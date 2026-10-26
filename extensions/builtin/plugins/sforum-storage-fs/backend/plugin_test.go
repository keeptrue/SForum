package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
)

func TestFSStorageRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SFORUM_SETTING_ROOT_PATH", root)
	t.Setenv("SFORUM_SETTING_PUBLIC_BASE_URL", "https://cdn.example/files")

	p := newFSStoragePlugin()
	probe, err := p.StorageProbe(pluginsdk.StorageProbeRequest{})
	if err != nil || !probe.OK {
		t.Fatalf("probe: %#v err=%v", probe, err)
	}

	payload := bytes.Repeat([]byte("chunk-data-"), 50)
	begin, err := p.StoragePutBegin(pluginsdk.StoragePutBeginRequest{
		Key:         "a/b/demo.bin",
		ContentType: "application/octet-stream",
		Size:        int64(len(payload)),
	})
	if err != nil || !begin.OK {
		t.Fatalf("put begin: %#v err=%v", begin, err)
	}

	// 小块模拟宿主分片。
	const chunk = 64
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		final := end == len(payload)
		res, err := p.StoragePutChunk(pluginsdk.StoragePutChunkRequest{
			SessionID: begin.SessionID,
			Data:      payload[off:end],
			Final:     final,
		})
		if err != nil || !res.OK {
			t.Fatalf("put chunk: %#v err=%v", res, err)
		}
	}

	exists, err := p.StorageExists(pluginsdk.StorageExistsRequest{Key: "a/b/demo.bin"})
	if err != nil || !exists.OK || !exists.Exists {
		t.Fatalf("exists: %#v err=%v", exists, err)
	}

	open, err := p.StorageOpen(pluginsdk.StorageOpenRequest{Key: "a/b/demo.bin"})
	if err != nil || !open.OK {
		t.Fatalf("open: %#v err=%v", open, err)
	}
	var got []byte
	for {
		chunkResp, err := p.StorageGetChunk(pluginsdk.StorageGetChunkRequest{
			SessionID: open.SessionID,
			MaxBytes:  48,
		})
		if err != nil || !chunkResp.OK {
			t.Fatalf("get chunk: %#v err=%v", chunkResp, err)
		}
		got = append(got, chunkResp.Data...)
		if chunkResp.EOF {
			break
		}
	}
	_, _ = p.StorageClose(pluginsdk.StorageCloseRequest{SessionID: open.SessionID})
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch got=%d want=%d", len(got), len(payload))
	}

	// 确认落盘路径在 root 下。
	if _, err := os.Stat(filepath.Join(root, "a", "b", "demo.bin")); err != nil {
		t.Fatalf("disk file: %v", err)
	}

	url, err := p.StoragePublicURL(pluginsdk.StoragePublicURLRequest{Key: "a/b/demo.bin"})
	if err != nil || url.URL != "https://cdn.example/files/a/b/demo.bin" {
		t.Fatalf("public url: %#v err=%v", url, err)
	}
	signed, err := p.StorageSignedURL(pluginsdk.StorageSignedURLRequest{Key: "a/b/demo.bin", TTLSeconds: 300})
	if err != nil || !signed.OK || signed.URL != "" {
		t.Fatalf("filesystem signed URL must be unsupported: %#v err=%v", signed, err)
	}

	del, err := p.StorageDelete(pluginsdk.StorageObjectRequest{Key: "a/b/demo.bin"})
	if err != nil || !del.OK {
		t.Fatalf("delete: %#v err=%v", del, err)
	}
}

func TestFSStorageRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SFORUM_SETTING_ROOT_PATH", root)
	p := newFSStoragePlugin()
	begin, err := p.StoragePutBegin(pluginsdk.StoragePutBeginRequest{Key: "../escape.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if begin.OK {
		t.Fatalf("expected invalid key, got %#v", begin)
	}
}

func TestFSStorageEmptyObject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SFORUM_SETTING_ROOT_PATH", root)
	p := newFSStoragePlugin()
	begin, _ := p.StoragePutBegin(pluginsdk.StoragePutBeginRequest{Key: "empty.txt"})
	if !begin.OK {
		t.Fatalf("%#v", begin)
	}
	res, _ := p.StoragePutChunk(pluginsdk.StoragePutChunkRequest{
		SessionID: begin.SessionID,
		Final:     true,
	})
	if !res.OK {
		t.Fatalf("%#v", res)
	}
	open, _ := p.StorageOpen(pluginsdk.StorageOpenRequest{Key: "empty.txt"})
	chunk, _ := p.StorageGetChunk(pluginsdk.StorageGetChunkRequest{SessionID: open.SessionID, MaxBytes: 16})
	if !chunk.OK || !chunk.EOF || len(chunk.Data) != 0 {
		t.Fatalf("%#v", chunk)
	}
	_ = io.Discard
}

func TestFSStorageProbeRequiresRoot(t *testing.T) {
	t.Setenv("SFORUM_SETTING_ROOT_PATH", "")
	p := newFSStoragePlugin()
	probe, err := p.StorageProbe(pluginsdk.StorageProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if probe.OK || probe.Reason != "storage.fs.config" {
		t.Fatalf("%#v", probe)
	}
}
