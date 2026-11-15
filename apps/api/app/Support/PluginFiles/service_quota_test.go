package pluginfiles

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 配额校验与落盘必须是同一个临界区（RV-53）。
//
// 旧实现是「Usage 读旧用量 → 判定 → 写临时文件 → rename」：并发写各自按同一份
// 旧用量判定，合计可以超过配额。这条用 quotaCheckHook 把第一个写卡在「已判定、
// 未落盘」的位置，再让第二个写进场；修复前第二个写会带着陈旧用量成功。
func TestWriteSerializesQuotaCheckWithinOneNamespace(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.EnsureNamespace(Namespace{ExtensionID: "demo.files", PrivateQuotaBytes: 100}); err != nil {
		t.Fatal(err)
	}

	firstIn := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	svc.quotaCheckHook = func() {
		once.Do(func() {
			close(firstIn)
			<-release
		})
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.Write(WriteRequest{
			ExtensionID: "demo.files", Kind: KindPrivate, RelativePath: "first.bin",
			Data: bytes.Repeat([]byte("a"), 80), Actor: "plugin",
		})
		firstDone <- err
	}()
	<-firstIn // 第一个写已通过配额校验，但还没落盘

	secondDone := make(chan error, 1)
	go func() {
		_, err := svc.Write(WriteRequest{
			ExtensionID: "demo.files", Kind: KindPrivate, RelativePath: "second.bin",
			Data: bytes.Repeat([]byte("b"), 80), Actor: "plugin",
		})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("the second write finished while the first still held the quota check (err=%v): both writers measured the same old usage", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := <-secondDone; !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second write = %v, want ErrQuotaExceeded (80+80 bytes against a 100-byte quota)", err)
	}
	usage, err := svc.Usage("demo.files")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.PrivateUsed > 100 {
		t.Fatalf("namespace usage = %d bytes, quota was 100: the write path broke the quota", usage.PrivateUsed)
	}
}

// 临时文件用随机名 + O_EXCL，写完后不得残留。
func TestWriteLeavesNoTemporaryArtifacts(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.EnsureNamespace(Namespace{ExtensionID: "demo.files"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Write(WriteRequest{
		ExtensionID: "demo.files", Kind: KindPrivate, RelativePath: "data/a.json",
		Data: []byte(`{"a":1}`), Actor: "plugin",
	}); err != nil {
		t.Fatal(err)
	}
	ns, err := svc.namespace("demo.files")
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	dirEntries, err := os.ReadDir(filepath.Join(ns.Root, KindPrivate, "data"))
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	names := make([]string, 0, len(dirEntries))
	for _, entry := range dirEntries {
		names = append(names, entry.Name())
	}
	if len(names) != 1 || names[0] != "a.json" {
		t.Fatalf("data dir entries = %v, want exactly [a.json] (no temp leftovers)", names)
	}
}
