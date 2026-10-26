# Security Policy / 安全策略

[简体中文](#简体中文) | [English](#english)

## 简体中文

### 支持版本

SForum 目前只为**最新正式发布版本**提供安全更新。安全修复会先进入
`main`，并根据影响范围发布新的修复版本。预发布版本仅用于测试，不应
用于生产环境；旧版本通常不会回移安全修复。

| 版本 | 安全支持 |
| --- | --- |
| 最新正式发布版本 | 支持 |
| `main` | 接收修复，不作为生产发布版本提供支持 |
| 预发布版本 | 尽力支持 |
| 更早的正式版本 | 不支持 |

请通过 [GitHub Releases](https://github.com/keeptrue/SForum/releases)
确认当前最新版本，并在报告中写明受影响的版本或提交。

### 私密报告漏洞

请勿通过公开 Issue、Discussion、Pull Request 或其他公开渠道披露尚未
修复的漏洞。

请发送邮件至 **zhuchunshu999@gmail.com**，邮件主题以
`[SForum Security]` 开头。报告建议包括：

- 受影响的 SForum 版本、提交以及组件；
- 部署环境和必要的配置条件；
- 可复现的步骤或最小化概念验证；
- 实际或潜在影响，以及已知的攻击前置条件；
- 已尝试的缓解措施；
- 可用于后续沟通的联系方式，以及期望的署名方式。

请仅提供确认问题所需的最少数据。不要发送真实用户凭据、访问令牌、
个人信息、完整数据库或生产环境转储；请先对敏感信息进行脱敏。如需
交换额外的敏感材料，请先通过邮件协商合适的传输方式。

一般缺陷、功能建议和不含敏感细节的加固建议可以使用
[GitHub Issues](https://github.com/keeptrue/SForum/issues)。如果无法
判断问题是否具有安全影响，请优先按安全漏洞私密报告。

### 处理流程

以下时间是维护目标，不是服务等级保证：

- 7 个自然日内确认收到报告；
- 14 个自然日内提供初步评估或请求补充信息；
- 确认漏洞后，与报告者协调修复、发布和公开披露时间；
- 处理时间较长时，通常每 14 个自然日同步一次重要进展。

维护者可能无法复现、认为问题不构成安全漏洞，或需要调整严重性等级。
遇到这些情况时，会尽量说明判断依据。修复发布后，维护者可在发行说明
或 GitHub Security Advisory 中致谢报告者；是否署名以报告者意愿为准。

### 协调披露

请在维护者发布修复或双方约定的披露日期之前保密。通常以首次报告后
90 天作为协调披露的最长目标窗口，但双方可以根据修复复杂度、受影响
用户范围和现实攻击情况另行约定。若发现漏洞正在被利用，请在报告中
明确说明，以便优先处理。

### 安全研究边界

我们欢迎出于善意、仅在你拥有或获准测试的系统上进行的研究。测试时请：

- 避免访问、修改或保留不属于你的数据；
- 避免破坏数据、降低服务可用性或对生产环境进行拒绝服务测试；
- 避免社会工程、垃圾信息、自动化高负载扫描和供应链投毒；
- 在证明安全影响后停止测试，不要扩大利用范围；
- 遵守适用法律以及相关第三方服务的规则。

SForum Core 和本仓库维护的内置扩展属于本策略范围。第三方扩展、外部
服务和下游修改应优先报告给各自维护者；如果问题源于 SForum 提供的
稳定接口或信任边界，也请同时报告给 SForum。

仅影响不受支持版本的问题、缺少可利用安全影响的自动扫描结果，以及
完全由文档已明确警告的不安全部署配置造成的问题，可能不会作为 SForum
漏洞处理。维护者仍欢迎包含清晰影响分析的加固建议。

### 获取安全更新

安全修复通过新的 SForum 版本发布，并记录在
[GitHub Releases](https://github.com/keeptrue/SForum/releases) 或
[GitHub Security Advisories](https://github.com/keeptrue/SForum/security/advisories)
中。部署者应升级到最新正式版本，并避免将分支名或浮动镜像标签作为可
复现的生产版本依据。

## English

### Supported Versions

SForum currently provides security updates for the **latest stable release
only**. Fixes land on `main` first and, depending on impact, are published in a
new patch release. Prereleases are for testing and should not be used in
production. Security fixes are not normally backported to older releases.

| Version | Security support |
| --- | --- |
| Latest stable release | Supported |
| `main` | Receives fixes; not a supported production release |
| Prereleases | Best effort |
| Older stable releases | Unsupported |

Check [GitHub Releases](https://github.com/keeptrue/SForum/releases) for the
current version, and identify the affected version or commit in your report.

### Reporting a Vulnerability Privately

Do not disclose an unpatched vulnerability in a public issue, discussion, pull
request, or other public channel.

Email **zhuchunshu999@gmail.com** with a subject beginning
`[SForum Security]`. A useful report includes:

- the affected SForum version, commit, and component;
- the deployment environment and required configuration;
- reproducible steps or a minimal proof of concept;
- the actual or potential impact and known prerequisites for exploitation;
- any mitigation you have tested; and
- contact details for follow-up and your preferred attribution.

Provide only the minimum data needed to confirm the issue. Do not send real
user credentials, access tokens, personal data, complete databases, or
production dumps. Redact sensitive values first. Contact the maintainer before
sending additional sensitive material so an appropriate transfer method can be
agreed.

Use [GitHub Issues](https://github.com/keeptrue/SForum/issues) for ordinary
bugs, feature requests, and hardening suggestions that contain no sensitive
details. If you are unsure whether an issue has security impact, report it
privately.

### What to Expect

These are response targets, not a service-level guarantee:

- acknowledgement within 7 calendar days;
- an initial assessment or request for more information within 14 calendar
  days;
- coordination with the reporter on remediation, release, and disclosure once
  the vulnerability is confirmed; and
- a material status update about every 14 calendar days when resolution takes
  longer.

The maintainer may be unable to reproduce a report, may determine that it is
not a security vulnerability, or may assign a different severity. When that
happens, the reasoning will be explained where practical. After a fix is
released, the reporter may be credited in release notes or a GitHub Security
Advisory according to their preference.

### Coordinated Disclosure

Keep the report confidential until a fix is released or an agreed disclosure
date is reached. The usual maximum target is 90 days from the initial report,
but the reporter and maintainer may agree on a different date based on the
complexity of the fix, affected users, and evidence of active exploitation.
Clearly identify suspected active exploitation so the report can be
prioritized.

### Safe Research Guidelines

Good-faith research is welcome only on systems you own or are authorized to
test. While testing:

- do not access, modify, or retain data that is not yours;
- do not destroy data, degrade availability, or perform denial-of-service
  testing against production systems;
- do not use social engineering, spam, high-volume automated scanning, or
  supply-chain poisoning;
- stop after demonstrating the security impact instead of expanding access;
  and
- comply with applicable law and the rules of any third-party service.

SForum Core and built-in extensions maintained in this repository are in
scope. Report vulnerabilities in third-party extensions, external services, or
downstream modifications to their respective maintainers first. Also report
the issue to SForum when it originates in an SForum contract or trust boundary.

Issues limited to unsupported releases, automated scan results without a
demonstrated exploitable impact, and deployments that rely entirely on
documented insecure configuration overrides may not be treated as SForum
vulnerabilities. Hardening suggestions with a clear impact analysis are still
welcome.

### Receiving Security Updates

Security fixes are shipped in new SForum releases and documented through
[GitHub Releases](https://github.com/keeptrue/SForum/releases) or
[GitHub Security Advisories](https://github.com/keeptrue/SForum/security/advisories).
Operators should upgrade to the latest stable release and avoid branch names or
floating image tags as the source of a reproducible production version.
