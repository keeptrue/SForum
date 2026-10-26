package extensionpackage

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"os"
	"runtime"
	"testing"
)

// 合成头只用于验证格式解析：真实产物由交叉编译或 os.Executable() 覆盖。

func syntheticELF(t *testing.T, machine elf.Machine, osabi elf.OSABI, class elf.Class, order binary.ByteOrder) []byte {
	t.Helper()
	buf := make([]byte, 64)
	copy(buf, "\x7fELF")
	buf[elf.EI_CLASS] = byte(class)
	if order == binary.LittleEndian {
		buf[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	} else {
		buf[elf.EI_DATA] = byte(elf.ELFDATA2MSB)
	}
	buf[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	buf[elf.EI_OSABI] = byte(osabi)
	order.PutUint16(buf[16:], uint16(elf.ET_EXEC))
	order.PutUint16(buf[18:], uint16(machine))
	order.PutUint32(buf[20:], 1)
	order.PutUint16(buf[52:], 64)
	return buf
}

func syntheticMachO(t *testing.T, cpu macho.Cpu) []byte {
	t.Helper()
	buf := make([]byte, 32)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], 0xFEEDFACF)
	le.PutUint32(buf[4:], uint32(cpu))
	le.PutUint32(buf[12:], 2) // MH_EXECUTE
	le.PutUint32(buf[16:], 0) // ncmds
	return buf
}

func syntheticFatMachO(t *testing.T, cpus []macho.Cpu) []byte {
	t.Helper()
	thins := make([][]byte, len(cpus))
	for i, cpu := range cpus {
		thins[i] = syntheticMachO(t, cpu)
	}
	headerSize := 8 + 20*len(cpus)
	buf := make([]byte, headerSize)
	be := binary.BigEndian
	be.PutUint32(buf[0:], 0xCAFEBABE)
	be.PutUint32(buf[4:], uint32(len(cpus)))
	offset := headerSize
	for offset%8 != 0 {
		buf = append(buf, 0)
		offset++
	}
	for i := range cpus {
		base := 8 + 20*i
		be.PutUint32(buf[base:], uint32(cpus[i]))
		be.PutUint32(buf[base+8:], uint32(offset))
		be.PutUint32(buf[base+12:], uint32(len(thins[i])))
		be.PutUint32(buf[base+16:], 3)
		buf = append(buf, thins[i]...)
		offset += len(thins[i])
	}
	return buf
}

func syntheticPE(t *testing.T, machine uint16) []byte {
	t.Helper()
	le := binary.LittleEndian
	buf := make([]byte, 0x100)
	copy(buf, "MZ")
	le.PutUint32(buf[0x3c:], 0x40)
	copy(buf[0x40:], []byte{'P', 'E', 0, 0})
	le.PutUint16(buf[0x44:], machine)
	// NumberOfSections=0 / SizeOfOptionalHeader=0 / 无符号表：只留头。
	return buf
}

func TestDetectTargetReadsHostExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatalf("open test binary: %v", err)
	}
	defer file.Close()

	target, ok := DetectTarget(file)
	if !ok {
		t.Fatal("expected the running test binary to be recognized")
	}
	if target.OS != runtime.GOOS || target.Arch != runtime.GOARCH {
		t.Fatalf("target = %s, want %s/%s", target, runtime.GOOS, runtime.GOARCH)
	}
}

func TestDetectTargetELF(t *testing.T) {
	cases := []struct {
		name    string
		machine elf.Machine
		osabi   elf.OSABI
		class   elf.Class
		order   binary.ByteOrder
		want    Target
	}{
		{
			name: "amd64 none osabi is unix-generic", machine: elf.EM_X86_64,
			osabi: elf.ELFOSABI_NONE, class: elf.ELFCLASS64, order: binary.LittleEndian,
			want: Target{Arch: "amd64"},
		},
		{
			name: "arm64", machine: elf.EM_AARCH64,
			osabi: elf.ELFOSABI_NONE, class: elf.ELFCLASS64, order: binary.LittleEndian,
			want: Target{Arch: "arm64"},
		},
		{
			name: "freebsd osabi is discriminating", machine: elf.EM_X86_64,
			osabi: elf.ELFOSABI_FREEBSD, class: elf.ELFCLASS64, order: binary.LittleEndian,
			want: Target{OS: "freebsd", Arch: "amd64"},
		},
		{
			name: "big endian ppc64", machine: elf.EM_PPC64,
			osabi: elf.ELFOSABI_NONE, class: elf.ELFCLASS64, order: binary.BigEndian,
			want: Target{Arch: "ppc64"},
		},
		{
			name: "little endian ppc64le", machine: elf.EM_PPC64,
			osabi: elf.ELFOSABI_NONE, class: elf.ELFCLASS64, order: binary.LittleEndian,
			want: Target{Arch: "ppc64le"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := syntheticELF(t, testCase.machine, testCase.osabi, testCase.class, testCase.order)
			target, ok := DetectTarget(bytes.NewReader(body))
			if !ok {
				t.Fatal("expected an ELF header to be recognized")
			}
			if target != testCase.want {
				t.Fatalf("target = %+v, want %+v", target, testCase.want)
			}
		})
	}
}

func TestDetectTargetMachO(t *testing.T) {
	target, ok := DetectTarget(bytes.NewReader(syntheticMachO(t, macho.CpuArm64)))
	if !ok {
		t.Fatal("expected a Mach-O header to be recognized")
	}
	if target != (Target{OS: "darwin", Arch: "arm64"}) {
		t.Fatalf("target = %+v", target)
	}
}

func TestDetectTargetFatMachOPrefersHostArch(t *testing.T) {
	other := macho.CpuAmd64
	if runtime.GOARCH == "amd64" {
		other = macho.CpuArm64
	}
	// 宿主架构排在第二：顺序不能决定结论。
	target, ok := DetectTarget(bytes.NewReader(syntheticFatMachO(t, []macho.Cpu{other, runtimeArchCPUMust(t)})))
	if !ok {
		t.Fatal("expected a fat Mach-O header to be recognized")
	}
	if target.Arch != runtime.GOARCH {
		t.Fatalf("target = %s, want host arch %s", target, runtime.GOARCH)
	}
	if target.OS != "darwin" {
		t.Fatalf("target OS = %q, want darwin", target.OS)
	}
}

func TestDetectTargetFatMachOFallsBackToFirstArch(t *testing.T) {
	target, ok := DetectTarget(bytes.NewReader(syntheticFatMachO(t, []macho.Cpu{macho.CpuAmd64, macho.CpuArm64})))
	if !ok {
		t.Fatal("expected a fat Mach-O header to be recognized")
	}
	want := "amd64"
	if runtime.GOARCH == "arm64" {
		want = "arm64"
	}
	if target.Arch != want {
		t.Fatalf("target arch = %s, want %s", target.Arch, want)
	}
}

func TestDetectTargetPE(t *testing.T) {
	target, ok := DetectTarget(bytes.NewReader(syntheticPE(t, 0x8664)))
	if !ok {
		t.Fatal("expected a PE header to be recognized")
	}
	if target != (Target{OS: "windows", Arch: "amd64"}) {
		t.Fatalf("target = %+v", target)
	}
}

func TestDetectTargetUnknownFormats(t *testing.T) {
	cases := map[string][]byte{
		"empty":         nil,
		"short":         []byte("\x7fEL"),
		"shell script":  []byte("#!/bin/sh\necho hi\n"),
		"plain text":    []byte("this is not an executable"),
		"truncated elf": []byte("\x7fELF\x02\x01\x01"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if target, ok := DetectTarget(bytes.NewReader(body)); ok {
				t.Fatalf("expected unknown, got %s", target)
			}
		})
	}
}

func TestDetectTargetNilReader(t *testing.T) {
	if target, ok := DetectTarget(nil); ok {
		t.Fatalf("expected unknown, got %s", target)
	}
}

func TestTargetCompatibleWith(t *testing.T) {
	host := Target{OS: "linux", Arch: "amd64"}
	cases := []struct {
		name     string
		detected Target
		want     bool
	}{
		{name: "exact match", detected: Target{OS: "linux", Arch: "amd64"}, want: true},
		{name: "arch mismatch", detected: Target{OS: "linux", Arch: "arm64"}, want: false},
		{name: "os mismatch", detected: Target{OS: "windows", Arch: "amd64"}, want: false},
		{name: "unknown os only checks arch", detected: Target{Arch: "amd64"}, want: true},
		{name: "unknown os still rejects other arch", detected: Target{Arch: "arm64"}, want: false},
		{name: "unknown host arch allows", detected: Target{OS: "linux"}, want: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.detected.CompatibleWith(host); got != testCase.want {
				t.Fatalf("CompatibleWith = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestTargetString(t *testing.T) {
	cases := map[Target]string{
		{OS: "linux", Arch: "amd64"}: "linux/amd64",
		{Arch: "amd64"}:              "any/amd64",
		{OS: "darwin"}:               "darwin/any",
		{}:                           "any/any",
	}
	for target, want := range cases {
		if got := target.String(); got != want {
			t.Fatalf("String(%+v) = %q, want %q", target, got, want)
		}
	}
}

// runtimeArchCPUMust 把 runtime.GOARCH 映射成对应的 Mach-O CPU 类型，供
// universal 二进制用例构造宿主架构。
func runtimeArchCPUMust(t *testing.T) macho.Cpu {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64":
		return macho.CpuAmd64
	case "arm64":
		return macho.CpuArm64
	default:
		t.Skipf("no Mach-O CPU mapping for host arch %s", runtime.GOARCH)
		return 0
	}
}
