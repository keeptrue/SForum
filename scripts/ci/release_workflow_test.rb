#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"
require "json"
require "open3"
require "tmpdir"

root = File.expand_path("../..", __dir__)
release = YAML.load_file(File.join(root, ".github/workflows/release.yml"))
ci = YAML.load_file(File.join(root, ".github/workflows/ci.yml"))
security = YAML.load_file(File.join(root, ".github/workflows/security.yml"))

def fail!(message)
  abort "release_workflow_test.rb: #{message}"
end

def needs(job)
  Array(job.fetch("needs", []))
end

def step_named(job, name)
  step = Array(job["steps"]).find { |candidate| candidate["name"] == name }
  fail!("missing workflow step: #{name}") unless step
  step
end

def glob_match?(pattern, path)
  expression = Regexp.escape(pattern)
    .gsub('\*\*', '.*')
    .gsub('\*', '[^/]*')
    .gsub('\?', '[^/]')
  Regexp.new("\\A#{expression}\\z").match?(path)
end

def run_script(script, environment)
  Dir.mktmpdir("sforum-workflow-test.") do |dir|
    output_path = File.join(dir, "github-output")
    stdout, stderr, status = Open3.capture3(
      environment.merge("GITHUB_OUTPUT" => output_path),
      "bash",
      "-c",
      script
    )
    unless status.success?
      fail!("workflow script failed: #{stdout}#{stderr}")
    end
    next {} unless File.exist?(output_path)

    File.read(output_path).lines.each_with_object({}) do |line, outputs|
      key, value = line.chomp.split("=", 2)
      outputs[key] = value
    end
  end
end

release_jobs = release.fetch("jobs")
publish = release_jobs.fetch("publish-candidate-platform")
matrix = publish.dig("strategy", "matrix", "include")
fail!("platform publish matrix must contain six entries") unless matrix.is_a?(Array) && matrix.length == 6

images = %w[sforum-api sforum-migrate sforum-web]
architectures = {
  "amd64" => ["linux/amd64", "ubuntu-latest"],
  "arm64" => ["linux/arm64", "ubuntu-24.04-arm"]
}

images.product(architectures.keys).each do |image, arch|
  entry = matrix.find { |candidate| candidate["image"] == image && candidate["arch"] == arch }
  fail!("missing #{image}/#{arch} native build") unless entry

  platform, runner = architectures.fetch(arch)
  fail!("#{image}/#{arch} has the wrong platform") unless entry["platform"] == platform
  fail!("#{image}/#{arch} has the wrong runner") unless entry["runner"] == runner

  expected_writer = %w[sforum-api sforum-web].include?(image)
  fail!("#{image}/#{arch} has the wrong cache writer policy") unless entry["cache_writer"] == expected_writer
end

release_jobs.each_value do |job|
  Array(job["steps"]).each do |step|
    uses = step["uses"].to_s
    fail!("release workflow must not use QEMU") if uses.include?("docker/setup-qemu-action")
  end
end

publish_steps = publish.fetch("steps")
build_index = publish_steps.index { |step| step["uses"].to_s.include?("docker/build-push-action") }
scan_index = publish_steps.index { |step| step["uses"].to_s.include?("aquasecurity/trivy-action") }
upload_index = publish_steps.index { |step| step["uses"].to_s.include?("actions/upload-artifact") }
fail!("platform build, scan, and digest upload steps are required") unless build_index && scan_index && upload_index
fail!("digest must be uploaded only after its platform scan passes") unless build_index < scan_index && scan_index < upload_index

build_inputs = publish_steps.fetch(build_index).fetch("with")
fail!("platform build must use the matrix platform") unless build_inputs["platforms"] == "${{ matrix.platform }}"
scan_environment = publish_steps.fetch(scan_index).fetch("env", {})
fail!("platform scan must use the matrix platform") unless scan_environment["TRIVY_PLATFORM"] == "${{ matrix.platform }}"
outputs = build_inputs["outputs"].to_s
unless outputs.include?("push-by-digest=true") && outputs.include?("name-canonical=true")
  fail!("platform build must push canonical digest-only candidates")
end

merge = release_jobs.fetch("merge-candidate")
fail!("manifest merge must wait for all platform candidates") unless needs(merge).include?("publish-candidate-platform")
fail!("manifest merge matrix must cover all three images") unless merge.dig("strategy", "matrix", "image") == images
promote = release_jobs.fetch("promote")
fail!("promotion matrix must cover only the three supported images") unless promote.dig("strategy", "matrix", "image") == images

sdk_publish = release_jobs.fetch("publish-web-sdks")
sdk_needs = needs(sdk_publish)
%w[prepare smoke verify-release-assets].each do |dependency|
  fail!("Web SDK publication must wait for #{dependency}") unless sdk_needs.include?(dependency)
end
sdk_permissions = sdk_publish.fetch("permissions", {})
unless sdk_permissions["contents"] == "read" && sdk_permissions["id-token"] == "write"
  fail!("Web SDK publication must use read-only contents plus npm Trusted Publishing OIDC")
end
sdk_steps = sdk_publish.fetch("steps")
setup_node = sdk_steps.find { |step| step["uses"].to_s.include?("actions/setup-node@") }
# setup-node 的 pin 是人工审查点：dependabot 把 action 升到新 SHA 时必须在这里同步，
# 并重新确认下方三个参数仍然满足 npm Trusted Publishing 的要求。放宽成前缀/标签匹配
# 会让这条闸门失去意义。
unless setup_node && setup_node["uses"] == "actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1" &&
       setup_node.dig("with", "node-version") == "24.19.0" &&
       setup_node.dig("with", "registry-url") == "https://registry.npmjs.org" &&
       setup_node.dig("with", "package-manager-cache") == false
  fail!("Web SDK publication must pin the reviewed Node 24/npm Trusted Publishing toolchain")
end
publish_command = sdk_steps.map { |step| step["run"] }.compact.find { |command| command.include?("publish-web-sdks.mjs") }
fail!("Web SDK publication command is missing") unless publish_command
fail!("promotion must wait for Web SDK publication") unless needs(promote).include?("publish-web-sdks")
fail!("Web SDK publication must not use a long-lived npm token") if sdk_publish.to_s.include?("NODE_AUTH_TOKEN")
sdk_publisher = File.read(File.join(root, "scripts/ci/publish-web-sdks.mjs"))
unless sdk_publisher.include?("--provenance") && sdk_publisher.include?("--access', 'public") &&
       sdk_publisher.include?("already exists with different content") &&
       sdk_publisher.include?("readRemoteContentIntegrity")
  fail!("Web SDK publisher must preserve provenance, public access, canonical retries, and immutable-version checks")
end

merge_command = merge.fetch("steps").map { |step| step["run"] }.compact.find do |command|
  command.include?("docker buildx imagetools create")
end

github_release = release_jobs.fetch("github-release")
release_command = github_release.fetch("steps").map { |step| step["run"] }.compact.find do |command|
  command.include?("gh release create")
end
fail!("GitHub release creation command is missing") unless release_command
unless release_command.include?("generate-release-notes.sh") &&
       release_command.include?("--notes-file") &&
       !release_command.include?("--generate-notes")
  fail!("GitHub releases must use the tested commit-list notes generator")
end
fail!("manifest merge command is missing") unless merge_command
unless merge_command.include?('sort == ["amd64", "arm64"]') && merge_command.include?("manifest_digest")
  fail!("manifest merge must verify both runtime architectures and export its digest")
end

smoke_needs = needs(release_jobs.fetch("smoke"))
fail!("smoke must wait for merged candidates") unless smoke_needs.include?("merge-candidate")
fail!("smoke must not consume unmerged platform candidates") if smoke_needs.include?("publish-candidate-platform")

darwin_assets = release_jobs.fetch("release-assets-darwin")
linux_assets = release_jobs.fetch("release-assets-linux")
fail!("legacy combined release-assets job must stay removed") if release_jobs.key?("release-assets")
unless needs(darwin_assets).sort == %w[prepare verify-ci]
  fail!("darwin release assets must depend only on prepare and verified CI")
end
unless needs(linux_assets).sort == %w[merge-candidate prepare]
  fail!("Linux release assets must wait for the merged scanned candidate")
end
unless darwin_assets.dig("permissions", "packages").nil?
  fail!("darwin release assets must not receive GHCR package permissions")
end
unless linux_assets.dig("permissions", "packages") == "read"
  fail!("Linux release assets need read-only GHCR access for scanned bundle extraction")
end
step_named(linux_assets, "Log in to GHCR for Linux bundle extraction")

darwin_asset_matrix = darwin_assets.dig("strategy", "matrix", "include")
expected_darwin_asset_matrix = [
  { "goos" => "darwin", "goarch" => "amd64" },
  { "goos" => "darwin", "goarch" => "arm64" }
]
unless darwin_asset_matrix == expected_darwin_asset_matrix
  fail!("darwin release assets must target only macOS on amd64/arm64")
end

linux_asset_matrix = linux_assets.dig("strategy", "matrix", "include")
expected_linux_asset_matrix = [
  { "goos" => "linux", "goarch" => "amd64" },
  { "goos" => "linux", "goarch" => "arm64" }
]
unless linux_asset_matrix == expected_linux_asset_matrix
  fail!("Linux release assets must target only Linux on amd64/arm64")
end

[darwin_assets, linux_assets].each do |asset_job|
  upload = step_named(asset_job, "Upload release assets")
  expected_name = "release-asset-${{ matrix.goos }}-${{ matrix.goarch }}"
  unless upload.dig("with", "name") == expected_name
    fail!("release asset artifact names must remain #{expected_name}")
  end
end

verify_assets = release_jobs.fetch("verify-release-assets")
expected_verify_needs = %w[deploy-asset prepare release-assets-darwin release-assets-linux]
unless needs(verify_assets).sort == expected_verify_needs
  fail!("release asset verification must aggregate darwin, Linux, and deploy assets")
end
download_assets = step_named(verify_assets, "Download release asset matrix")
unless download_assets.dig("with", "pattern") == "release-asset-*"
  fail!("release asset verification must keep the release-asset-* download pattern")
end
step_named(verify_assets, "Verify release assets and generate checksums")
step_named(verify_assets, "Attest release assets")
verified_upload = step_named(verify_assets, "Upload verified release assets")
unless verified_upload.dig("with", "name") == "verified-release-assets"
  fail!("release asset verification must keep publishing verified-release-assets")
end

ci_jobs = ci.fetch("jobs")
fail!("CI must not keep a duplicate Web dependency build") if ci_jobs.key?("web-runtime-vulnerabilities")

ci_source = File.read(File.join(root, ".github/workflows/ci.yml"))
push_trigger = ci_source[/^  push:\n(.*?)(?=^  [a-z_]+:|\z)/m, 1]
fail!("CI push trigger must not use paths") if push_trigger&.match?(/^\s+paths:/)

changes = ci_jobs.fetch("changes")
expected_change_outputs = %w[go web contracts container_api container_web]
expected_change_outputs.each do |output|
  fail!("CI changes job must publish #{output}") unless changes.fetch("outputs", {}).key?(output)
end
filter_step = step_named(changes, "Detect changed paths")
filters = YAML.safe_load(filter_step.dig("with", "filters"))
publish_step = step_named(changes, "Publish change outputs")

change_scenarios = {
  "docs-only" => {
    "files" => ["docs/ci.md"],
    "expected" => {
      "go" => "false",
      "web" => "false",
      "contracts" => "true",
      "container_api" => "false",
      "container_web" => "false",
      "images" => []
    }
  },
  "go-only" => {
    "files" => ["apps/api/cmd/api/main.go"],
    "expected" => {
      "go" => "true",
      "web" => "false",
      "contracts" => "true",
      "container_api" => "true",
      "container_web" => "false",
      "images" => ["api"]
    }
  },
  "web-only" => {
    "files" => ["apps/web/app/pages/index.vue"],
    "expected" => {
      "go" => "false",
      "web" => "true",
      "contracts" => "true",
      "container_api" => "false",
      "container_web" => "true",
      "images" => ["web"]
    }
  }
}

change_scenarios.each do |label, scenario|
  matched = filters.transform_values do |patterns|
    scenario.fetch("files").any? do |path|
      patterns.any? { |pattern| glob_match?(pattern, path) }
    end
  end
  outputs = run_script(
    publish_step.fetch("run"),
    {
      "EVENT_NAME" => "push",
      "FILTER_GO" => matched.fetch("go").to_s,
      "FILTER_WEB" => matched.fetch("web").to_s,
      "FILTER_CONTRACTS" => matched.fetch("contracts").to_s,
      "FILTER_CONTAINER_API" => matched.fetch("container_api").to_s,
      "FILTER_CONTAINER_WEB" => matched.fetch("container_web").to_s
    }
  )
  expected = scenario.fetch("expected")
  expected.each do |key, value|
    next if key == "images"
    fail!("#{label} changes output #{key} was #{outputs[key]}") unless outputs[key] == value
  end
  images = JSON.parse(outputs.fetch("container_images")).map { |entry| entry.fetch("image") }
  fail!("#{label} selected containers #{images}") unless images == expected.fetch("images")
end

%w[workflow_call workflow_dispatch].each do |event_name|
  outputs = run_script(
    publish_step.fetch("run"),
    {
      "EVENT_NAME" => event_name,
      "FILTER_GO" => "",
      "FILTER_WEB" => "",
      "FILTER_CONTRACTS" => "",
      "FILTER_CONTAINER_API" => "",
      "FILTER_CONTAINER_WEB" => ""
    }
  )
  %w[go web contracts container_api container_web].each do |key|
    fail!("#{event_name} must default #{key} to true") unless outputs[key] == "true"
  end
  images = JSON.parse(outputs.fetch("container_images")).map { |entry| entry.fetch("image") }
  fail!("#{event_name} must default both containers") unless images == %w[api web]
end

%w[go web contracts].each do |job_name|
  job = ci_jobs.fetch(job_name)
  fail!("#{job_name} must depend only on the changes detector") unless needs(job) == ["changes"]
  fail!("#{job_name} must be gated by its changes output") unless job["if"].to_s.include?("needs.changes.outputs.#{job_name} == 'true'")
end

contracts_run = ci_jobs.fetch("contracts").fetch("steps").find do |step|
  step["name"] == "Run contracts and static gates"
end
fail!("contracts gate step is missing") unless contracts_run
skip_web_unit = contracts_run.dig("env", "SFORUM_CONTRACTS_SKIP_WEB_UNIT").to_s
unless skip_web_unit.include?("needs.changes.outputs.web == 'true'")
  fail!("contracts may skip duplicated Web unit tests only when the Web gate runs")
end

containers = ci_jobs.fetch("containers")
container_needs = needs(containers)
fail!("container builds may depend only on the changes detector") unless container_needs == ["changes"]
fail!("container matrix must be selected by changes") unless containers.dig("strategy", "matrix", "include").to_s.include?(
  "fromJSON(needs.changes.outputs.container_images)"
)

container_steps = containers.fetch("steps")
build_index = container_steps.index do |step|
  step["uses"].to_s.include?("docker/build-push-action")
end
scan_index = container_steps.index do |step|
  step["uses"].to_s.include?("aquasecurity/trivy-action")
end
fail!("CI container build and Web image scan are required") unless build_index && scan_index
fail!("Web image must be scanned after it is built") unless build_index < scan_index

container_build = container_steps.fetch(build_index)
build_inputs = container_build.fetch("with")
unless build_inputs["load"] == "${{ matrix.image == 'web' }}" &&
       build_inputs["tags"] == "${{ matrix.image == 'web' && 'sforum-web:ci' || '' }}"
  fail!("CI must load only the Web image under a stable local tag")
end

scan = container_steps.fetch(scan_index)
scan_condition = scan["if"].to_s
fail!("CI image scan must only run for Web") unless scan_condition.include?("matrix.image == 'web'")
scan_inputs = scan.fetch("with")
unless scan_inputs["scan-type"] == "image" &&
       scan_inputs["image-ref"] == "sforum-web:ci" &&
       scan_inputs["vuln-type"] == "os,library" &&
       scan_inputs["severity"] == "CRITICAL,HIGH"
  fail!("CI Web scan must check CRITICAL/HIGH vulnerabilities in the final runtime image")
end
# 扫描口径（2026-10-19 起）：CI 里的 Web 镜像扫描**只报告不阻断**。第三方基础镜像
# 的 CVE 不属于 PR 作者改动范围，用它对无关 PR 阻断会让整条流水线长期为别人的漏洞变红；
# PR 引入的新增依赖漏洞由 Security workflow 的 dependency-review 覆盖。
# 因此这里断言的是「不存在阻断式扫描」，而不是「必须存在」。
blocking_scan = container_steps.find do |step|
  step["uses"].to_s.include?("aquasecurity/trivy-action") && step["with"]["exit-code"].to_s == "1"
end
fail!("CI Web image scan must be report-only (no blocking trivy step)") if blocking_scan

cache_to = build_inputs["cache-to"].to_s
unless cache_to.include?("github.event_name != 'pull_request'")
  fail!("pull requests must not export max-mode container caches")
end

summary = ci_jobs.fetch("ci-summary")
fail!("CI summary must run even when heavy jobs are skipped") unless summary["if"] == "always()"
expected_summary_needs = %w[changes go web contracts containers]
unless needs(summary).sort == expected_summary_needs.sort
  fail!("CI summary must cover changes and every downstream gate")
end
summary_step = step_named(summary, "Verify downstream job outcomes")
summary_success = run_script(
  summary_step.fetch("run"),
  {
    "CHANGES_RESULT" => "success",
    "GO_RESULT" => "skipped",
    "WEB_RESULT" => "success",
    "CONTRACTS_RESULT" => "success",
    "CONTAINERS_RESULT" => "skipped"
  }
)
fail!("CI summary must accept success and skipped jobs") unless summary_success.empty?
stdout, stderr, status = Open3.capture3(
  {
    "CHANGES_RESULT" => "success",
    "GO_RESULT" => "failure",
    "WEB_RESULT" => "skipped",
    "CONTRACTS_RESULT" => "skipped",
    "CONTAINERS_RESULT" => "skipped"
  },
  "bash",
  "-c",
  summary_step.fetch("run")
)
fail!("CI summary must reject a failed downstream job") if status.success?
fail!("CI summary did not identify the failed job") unless "#{stdout}#{stderr}".include?("go job ended with failure")

security_jobs = security.fetch("jobs")
security_changes = security_jobs.fetch("changes")
%w[go web web_audit codeql_languages].each do |output|
  fail!("Security changes job must publish #{output}") unless security_changes.fetch("outputs", {}).key?(output)
end
security_filters = YAML.safe_load(step_named(security_changes, "Detect changed paths").dig("with", "filters"))
security_publish = step_named(security_changes, "Publish security outputs")
security_scenarios = {
  "docs-only" => {
    "files" => ["docs/security.md"],
    "expected" => {
      "go" => "false",
      "web" => "false",
      "web_audit" => "false",
      "languages" => []
    }
  },
  "go-only" => {
    "files" => ["apps/api/cmd/api/main.go"],
    "expected" => {
      "go" => "true",
      "web" => "false",
      "web_audit" => "false",
      "languages" => ["go"]
    }
  },
  "web-only" => {
    "files" => ["apps/web/app/pages/index.vue"],
    "expected" => {
      "go" => "false",
      "web" => "true",
      "web_audit" => "false",
      "languages" => ["javascript-typescript"]
    }
  },
  "web-audit" => {
    "files" => ["apps/web/bun.lock"],
    "expected" => {
      "go" => "false",
      "web" => "true",
      "web_audit" => "true",
      "languages" => ["javascript-typescript"]
    }
  }
}
security_scenarios.each do |label, scenario|
  matched = security_filters.transform_values do |patterns|
    scenario.fetch("files").any? do |path|
      patterns.any? { |pattern| glob_match?(pattern, path) }
    end
  end
  outputs = run_script(
    security_publish.fetch("run"),
    {
      "EVENT_NAME" => "pull_request",
      "FILTER_GO" => matched.fetch("go").to_s,
      "FILTER_WEB" => matched.fetch("web").to_s,
      "FILTER_WEB_AUDIT" => matched.fetch("web_audit").to_s
    }
  )
  expected = scenario.fetch("expected")
  expected.each do |key, value|
    next if key == "languages"
    fail!("security #{label} output #{key} was #{outputs[key]}") unless outputs[key] == value
  end
  languages = JSON.parse(outputs.fetch("codeql_languages")).map { |entry| entry.fetch("language") }
  fail!("security #{label} selected CodeQL languages #{languages}") unless languages == expected.fetch("languages")
end

%w[schedule workflow_dispatch].each do |event_name|
  outputs = run_script(
    security_publish.fetch("run"),
    {
      "EVENT_NAME" => event_name,
      "FILTER_GO" => "",
      "FILTER_WEB" => "",
      "FILTER_WEB_AUDIT" => ""
    }
  )
  %w[go web web_audit].each do |key|
    fail!("security #{event_name} must default #{key} to true") unless outputs[key] == "true"
  end
  languages = JSON.parse(outputs.fetch("codeql_languages")).map { |entry| entry.fetch("language") }
  fail!("security #{event_name} must default both CodeQL languages") unless languages == %w[go javascript-typescript]
end

codeql = security_jobs.fetch("codeql")
fail!("CodeQL must depend only on security changes") unless needs(codeql) == ["changes"]
fail!("CodeQL languages must be selected by security changes") unless codeql["if"].to_s.include?(
  "needs.changes.outputs.codeql_languages != '[]'"
)
unless needs(security_jobs.fetch("govulncheck")) == ["changes"] &&
       security_jobs.fetch("govulncheck")["if"].to_s.include?("needs.changes.outputs.go == 'true'")
  fail!("Govulncheck must be gated by Go changes")
end
unless needs(security_jobs.fetch("web-audit")) == ["changes"] &&
       security_jobs.fetch("web-audit")["if"].to_s.include?("needs.changes.outputs.web_audit == 'true'")
  fail!("Web audit must be gated by Web dependency changes")
end

smoke_script = File.read(File.join(root, "scripts/ci/release-smoke.sh"))
unless smoke_script.include?('docker run --rm "$image" "$binary" --version') &&
       smoke_script.include?('image="${SFORUM_REGISTRY}/sforum-${service}:${SFORUM_VERSION}"')
  fail!("image identity checks must run without Compose service dependencies")
end

puts "release_workflow_test.rb: all checks passed"
