package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
)

const (
	// 探测 / Stat / Delete / 预签名等元数据操作的有界超时。
	metadataOperationTimeout = 60 * time.Second
	// 上传发生在 Host put_chunk RPC 的收尾调用里，对齐 manifest
	// providers[].timeoutMs（Host 侧上限 120s）。
	uploadOperationTimeout = 120 * time.Second
	defaultRegion          = "us-east-1"
	r2Region               = "auto"
)

type s3Backend struct {
	client        *s3.Client
	presign       *s3.PresignClient
	bucket        string
	prefix        string
	publicBaseURL string
}

func newS3Backend(values map[string]string) (pluginsdk.StorageBackend, error) {
	bucket := strings.TrimSpace(values["bucket"])
	if bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(values["endpoint"]), "/")
	if endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("endpoint must be an absolute http or https URL")
		}
	}
	accessKey, secretKey := strings.TrimSpace(values["access_key_id"]), strings.TrimSpace(values["secret_access_key"])
	if (accessKey == "") != (secretKey == "") {
		return nil, fmt.Errorf("access key id and secret access key must be configured together")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(resolveRegion(values["region"], endpoint))}
	if accessKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, strings.TrimSpace(values["session_token"]))))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	// 读取/上传流的存活期不受整体期限约束：连接建立与首包等待由传输层超时兜底，
	// 读取终止由 Host 的 RPC 会话 Close 负责，大对象代理下载不再被固定期限截断。
	cfg.HTTPClient = newStorageHTTPClient()
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		// R2 等第三方兼容服务会拒绝 SDK 默认附带的 CRC32 请求校验和
		// （x-amz-checksum-crc32 / aws-chunked trailer）；只在 API 明确要求时
		// 计算与校验，AWS S3 与 MinIO 行为不受影响。
		options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		options.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		options.UsePathStyle = resolveUsePathStyle(values["use_path_style"], endpoint)
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
	prefix := strings.Trim(strings.ReplaceAll(strings.TrimSpace(values["object_prefix"]), "\\", "/"), "/")
	if prefix == "." || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("object prefix is invalid")
	}
	return &s3Backend{client: client, presign: s3.NewPresignClient(client), bucket: bucket, prefix: prefix, publicBaseURL: strings.TrimRight(strings.TrimSpace(values["public_base_url"]), "/")}, nil
}

// newStorageHTTPClient 只约束连接建立与响应头等待，不设客户端总超时。
func newStorageHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// resolveRegion 尊重显式配置；留空且端点是 R2 时使用官方要求的 auto，其余回落 AWS 默认。
func resolveRegion(raw, endpoint string) string {
	if region := strings.TrimSpace(raw); region != "" {
		return region
	}
	if isR2Endpoint(endpoint) {
		return r2Region
	}
	return defaultRegion
}

// resolveUsePathStyle 支持显式 true/false；留空或 auto 时自动判定：
// 配置了非 AWS 端点（R2、MinIO 及多数第三方网关）用 path-style，
// AWS（无端点或 amazonaws.com）保持 virtual-host。
func resolveUsePathStyle(raw, endpoint string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true
	case "false":
		return false
	default:
		return isCustomEndpoint(endpoint)
	}
}

func isCustomEndpoint(endpoint string) bool {
	host, ok := endpointHost(endpoint)
	if !ok {
		return false
	}
	return host != "amazonaws.com" && !strings.HasSuffix(host, ".amazonaws.com")
}

func isR2Endpoint(endpoint string) bool {
	host, ok := endpointHost(endpoint)
	return ok && strings.HasSuffix(host, ".r2.cloudflarestorage.com")
}

func endpointHost(endpoint string) (string, bool) {
	if endpoint == "" {
		return "", false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return strings.ToLower(parsed.Hostname()), true
}

func (b *s3Backend) Probe() error {
	key := b.key(".sforum-probe/" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".txt")
	payload := []byte("sforum-storage-probe")
	ctx, cancel := context.WithTimeout(context.Background(), metadataOperationTimeout)
	defer cancel()
	if _, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &b.bucket, Key: &key, Body: bytes.NewReader(payload), ContentType: aws.String("text/plain")}); err != nil {
		return fmt.Errorf("write probe object: %w", err)
	}
	defer func() {
		_, _ = b.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: &b.bucket, Key: &key})
	}()
	if _, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.bucket, Key: &key}); err != nil {
		return fmt.Errorf("stat probe object: %w", err)
	}
	object, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &b.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("read probe object: %w", err)
	}
	got, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read probe body: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close probe body: %w", closeErr)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("probe object content mismatch")
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.bucket, Key: &key}); err != nil {
		return fmt.Errorf("delete probe object: %w", err)
	}
	return nil
}

func (b *s3Backend) Put(key, contentType string, reader io.Reader) error {
	ctx, cancel := context.WithTimeout(context.Background(), uploadOperationTimeout)
	defer cancel()
	objectKey := b.key(key)
	if _, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &b.bucket, Key: &objectKey, Body: reader, ContentType: optionalString(contentType)}); err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (b *s3Backend) Open(key string) (io.ReadCloser, error) {
	objectKey := b.key(key)
	// GetObject 使用不带整体期限的 context：body 读取时长由 Host 会话 Close 控制，
	// 请求发起阶段的挂起由共享传输的 ResponseHeaderTimeout 兜底。
	result, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: &b.bucket, Key: &objectKey})
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	return result.Body, nil
}

func (b *s3Backend) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), metadataOperationTimeout)
	defer cancel()
	objectKey := b.key(key)
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.bucket, Key: &objectKey}); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (b *s3Backend) Stat(key string) (pluginsdk.StorageObjectInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), metadataOperationTimeout)
	defer cancel()
	objectKey := b.key(key)
	result, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.bucket, Key: &objectKey})
	if isNotFound(err) {
		return pluginsdk.StorageObjectInfo{Exists: false}, nil
	}
	if err != nil {
		return pluginsdk.StorageObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	info := pluginsdk.StorageObjectInfo{Exists: true, ContentType: aws.ToString(result.ContentType)}
	if result.ContentLength != nil {
		info.Size = *result.ContentLength
	}
	if result.LastModified != nil {
		info.ModifiedUnix = result.LastModified.Unix()
	}
	return info, nil
}

func (b *s3Backend) Exists(key string) (bool, error) {
	info, err := b.Stat(key)
	return info.Exists, err
}

func (b *s3Backend) PublicURL(key string) string {
	if b.publicBaseURL == "" {
		return ""
	}
	return pluginsdk.JoinStoragePublicURL(b.publicBaseURL, b.key(key))
}

func (b *s3Backend) SignedURL(key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), metadataOperationTimeout)
	defer cancel()
	objectKey := b.key(key)
	result, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &b.bucket, Key: &objectKey}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign get object: %w", err)
	}
	return result.URL, nil
}

func (b *s3Backend) key(key string) string {
	if b.prefix == "" {
		return key
	}
	return path.Join(b.prefix, key)
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NoSuchBucket"
	}
	return false
}
