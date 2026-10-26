package extensionpackage

import (
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"io"
	"runtime"
	"strings"
)

// Target 描述一份可执行制品的目标平台。
//
// 空字段表示「不知道」，不是「不匹配」：
//   - OS 为空：当前容器格式区分不出操作系统。ELF 的 OSABI 只有
//     FreeBSD/NetBSD/OpenBSD 三个可判别取值，Go 给 linux/android/solaris/
//     illumos/dragonfly 产出的二进制一律是 ELFOSABI_NONE；Mach-O 也分不出
//     darwin 与 ios。
//   - Arch 为空：连机器类型都读不出来。
//
// 调用方对空字段一律按「未知即放行」处理，把最终裁判交给 Core 原有的进程启动
// 诊断（exec format error），而不是在这里制造假阳性。
type Target struct {
	OS   string
	Arch string
}

// String 渲染 "os/arch"。未知的一侧写成 any，便于直接进错误字段、日志和
// 管理端文案插值。
func (t Target) String() string {
	os, arch := t.OS, t.Arch
	if os == "" {
		os = "any"
	}
	if arch == "" {
		arch = "any"
	}
	return os + "/" + arch
}

// CompatibleWith 报告这份制品能否在 host 上运行。任一维度的「不知道」都不构成
// 拒绝理由：那是探测能力的边界，不是包的缺陷。
func (t Target) CompatibleWith(host Target) bool {
	if t.Arch != "" && host.Arch != "" && t.Arch != host.Arch {
		return false
	}
	if t.OS != "" && host.OS != "" && t.OS != host.OS {
		return false
	}
	return true
}

// HostTarget 是当前进程的平台，作为 CompatibleWith 的比较基准。
func HostTarget() Target {
	return Target{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// DetectTarget 只读文件头，判定这份可执行制品是为哪个平台构建的。
//
// 判定顺序是刻意的：
//  1. fat Mach-O 先走：universal 二进制里任一架构都能跑，必须优先挑中宿主
//     架构。若交给 buildinfo 先看，它只读 Arches[0]，会把「先 amd64 后 arm64」
//     的包在 arm64 宿主上误判成不兼容。
//  2. Go 二进制走 buildinfo：GOOS/GOARCH 是链接器写进二进制的精确事实，比容器
//     格式可靠得多，而且不受 OSABI 歧义影响。
//  3. 再按容器格式兜底：ELF → 薄 Mach-O → PE。
//
// 脚本、文本、空文件和无法识别的格式返回 false（「不知道」）。函数不执行任何
// 包代码，只读文件头。
func DetectTarget(r io.ReaderAt) (Target, bool) {
	if r == nil {
		return Target{}, false
	}
	if target, ok := detectFatMachO(r); ok {
		return target, true
	}
	if target, ok := detectGoBuildInfo(r); ok {
		return target, true
	}
	if target, ok := detectELF(r); ok {
		return target, true
	}
	if target, ok := detectMachO(r); ok {
		return target, true
	}
	if target, ok := detectPE(r); ok {
		return target, true
	}
	return Target{}, false
}

// detectGoBuildInfo 读链接器写入的 GOOS/GOARCH。非 Go 二进制会返回 false，
// 交给容器格式兜底。
func detectGoBuildInfo(r io.ReaderAt) (Target, bool) {
	info, err := buildinfo.Read(r)
	if err != nil {
		return Target{}, false
	}
	var target Target
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			target.OS = normalizePlatformPart(setting.Value)
		case "GOARCH":
			target.Arch = normalizePlatformPart(setting.Value)
		}
	}
	if target.Arch == "" {
		return Target{}, false
	}
	return target, true
}

func detectELF(r io.ReaderAt) (Target, bool) {
	file, err := elf.NewFile(r)
	if err != nil {
		return Target{}, false
	}
	defer file.Close()
	arch := elfArch(file)
	if arch == "" {
		return Target{}, false
	}
	return Target{OS: elfOS(file.OSABI), Arch: arch}, true
}

func detectMachO(r io.ReaderAt) (Target, bool) {
	file, err := macho.NewFile(r)
	if err != nil {
		return Target{}, false
	}
	defer file.Close()
	arch := machoArch(file.Cpu)
	if arch == "" {
		return Target{}, false
	}
	return Target{OS: "darwin", Arch: arch}, true
}

// detectFatMachO 处理 universal 二进制：任一包含的架构都能在本机运行，
// 因此优先命中宿主架构，其次退回第一个可识别的架构。
func detectFatMachO(r io.ReaderAt) (Target, bool) {
	fat, err := macho.NewFatFile(r)
	if err != nil || len(fat.Arches) == 0 {
		return Target{}, false
	}
	defer fat.Close()
	fallback := ""
	for _, arch := range fat.Arches {
		name := machoArch(arch.Cpu)
		if name == "" {
			continue
		}
		if name == runtime.GOARCH {
			return Target{OS: "darwin", Arch: name}, true
		}
		if fallback == "" {
			fallback = name
		}
	}
	if fallback == "" {
		return Target{}, false
	}
	return Target{OS: "darwin", Arch: fallback}, true
}

func detectPE(r io.ReaderAt) (Target, bool) {
	file, err := pe.NewFile(r)
	if err != nil {
		return Target{}, false
	}
	defer file.Close()
	arch := peArch(file.Machine)
	if arch == "" {
		return Target{}, false
	}
	return Target{OS: "windows", Arch: arch}, true
}

// elfOS 只返回 OSABI 能唯一确定的操作系统。ELFOSABI_NONE 被 linux、android、
// solaris、illumos 和 dragonfly 共用，所以它表示「不知道」，不是「linux」——
// 猜成 linux 会在非 linux 的 ELF 宿主上把合法包判死。
func elfOS(osabi elf.OSABI) string {
	switch osabi {
	case elf.ELFOSABI_FREEBSD:
		return "freebsd"
	case elf.ELFOSABI_NETBSD:
		return "netbsd"
	case elf.ELFOSABI_OPENBSD:
		return "openbsd"
	case elf.ELFOSABI_SOLARIS:
		return "solaris"
	case elf.ELFOSABI_LINUX:
		return "linux"
	default:
		return ""
	}
}

func elfArch(file *elf.File) string {
	little := file.ByteOrder == binary.LittleEndian
	switch file.Machine {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_386:
		return "386"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_PPC64:
		if little {
			return "ppc64le"
		}
		return "ppc64"
	case elf.EM_S390:
		return "s390x"
	case elf.EM_RISCV:
		return "riscv64"
	case elf.EM_LOONGARCH:
		return "loong64"
	case elf.EM_MIPS:
		name := "mips"
		if file.Class == elf.ELFCLASS64 {
			name = "mips64"
		}
		if little {
			return name + "le"
		}
		return name
	default:
		return ""
	}
}

func machoArch(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	case macho.Cpu386:
		return "386"
	case macho.CpuArm:
		return "arm"
	case macho.CpuPpc64:
		return "ppc64"
	default:
		return ""
	}
}

func peArch(machine uint16) string {
	switch machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return "amd64"
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return "arm64"
	case pe.IMAGE_FILE_MACHINE_I386:
		return "386"
	case pe.IMAGE_FILE_MACHINE_ARM, pe.IMAGE_FILE_MACHINE_ARMNT:
		return "arm"
	default:
		return ""
	}
}

func normalizePlatformPart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
