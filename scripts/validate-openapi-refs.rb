#!/usr/bin/env ruby
# frozen_string_literal: true

require 'json'
require 'pathname'
require 'set'
require 'yaml'

ROOT = Pathname.new(__dir__).join('..').expand_path
ENTRY = ROOT.join('contracts/openapi.yaml')

# --- 重复键扫描 -----------------------------------------------------------------
#
# YAML 的「后键覆盖」会把同一 mapping 里的重复键静默吃掉。openapi.yaml 里把两个
# $ref 写在同一个路径键下面时，第二个会顶掉第一个：结果一个文件还连着入口、另一个
# 变成孤儿，而 $ref 解析和路由绑定两遍检查仍然全绿（RV-11 就是这样漏过去的）。
# 解析成 Hash 之后再查已经太晚——重复键在 Hash 里根本不存在了，所以这里在解析之前
# 直接走 AST，并打印文件、行号与键路径。
class DuplicateKeyScanner
  def initialize(root)
    @root = root
  end

  # 返回 [{path:, line:, first_line:, key_path:, key:}]；空数组表示干净。
  def scan(path)
    stream = Psych.parse_stream(path.read, filename: path.to_s)
    findings = []
    walk(stream, [], findings, path)
    findings
  rescue Psych::SyntaxError => e
    abort "OpenAPI YAML syntax error in #{relative(path)}: #{e.message}"
  end

  private

  def walk(node, key_path, findings, path)
    case node
    when Psych::Nodes::Mapping
      seen = {}
      node.children.each_slice(2) do |key_node, value_node|
        unless key_node.is_a?(Psych::Nodes::Scalar)
          walk(value_node, key_path, findings, path)
          next
        end
        key = key_node.value
        line = key_node.start_line + 1
        if seen.key?(key)
          findings << {
            path: path,
            line: line,
            first_line: seen[key],
            key: key,
            key_path: (key_path + [key]).join('.')
          }
        else
          seen[key] = line
        end
        walk(value_node, key_path + [key], findings, path)
      end
    when Psych::Nodes::Sequence
      node.children.each_with_index { |child, index| walk(child, key_path + ["[#{index}]"], findings, path) }
    when Psych::Nodes::Stream, Psych::Nodes::Document
      node.children.each { |child| walk(child, key_path, findings, path) }
    end
  end

  def relative(path)
    path.relative_path_from(@root)
  rescue ArgumentError
    path
  end
end

# 这里只做本地拆分文件的 YAML/$ref 完整性检查；完整 OpenAPI 规范校验
# 以后可以交给 Redocly CLI 或 OpenAPI Generator 这类专用工具。
class RefValidator
  def initialize(root)
    @root = root
    @cache = {}
    @checked_refs = 0
  end

  attr_reader :checked_refs

  # 第二遍（路由绑定校验）复用同一套 $ref 解析：path item 也可以是 $ref。
  def resolve_reference(base_path, ref)
    resolve_ref(base_path, ref)
  end

  def validate_file(path)
    document = load_yaml(path)
    each_ref(document) do |ref|
      resolve_ref(path, ref)
      @checked_refs += 1
    end
  end

  private

  def load_yaml(path)
    absolute = path.expand_path
    @cache[absolute.to_s] ||= YAML.load_file(absolute.to_s)
  rescue Errno::ENOENT
    abort "OpenAPI reference target does not exist: #{relative(absolute)}"
  rescue Psych::SyntaxError => e
    abort "OpenAPI YAML syntax error in #{relative(absolute)}: #{e.message}"
  end

  def each_ref(value, &block)
    case value
    when Hash
      value.each do |key, child|
        yield child if key == '$ref' && child.is_a?(String)
        each_ref(child, &block)
      end
    when Array
      value.each { |child| each_ref(child, &block) }
    end
  end

  def resolve_ref(base_path, ref)
    file_part, fragment = ref.split('#', 2)
    target_path = if file_part.nil? || file_part.empty?
                    base_path.expand_path
                  else
                    base_path.dirname.join(file_part).cleanpath.expand_path
                  end

    document = load_yaml(target_path)
    resolve_fragment(target_path, document, fragment)
  end

  def resolve_fragment(target_path, document, fragment)
    return document if fragment.nil? || fragment.empty?

    unless fragment.start_with?('/')
      abort "Unsupported OpenAPI fragment in #{relative(target_path)}: ##{fragment}"
    end

    fragment.split('/')[1..].each do |raw_segment|
      segment = raw_segment.gsub('~1', '/').gsub('~0', '~')
      if document.is_a?(Hash) && document.key?(segment)
        document = document[segment]
      elsif document.is_a?(Array) && segment.match?(/\A\d+\z/) && document[segment.to_i]
        document = document[segment.to_i]
      else
        abort "Broken OpenAPI reference in #{relative(target_path)}: ##{fragment}"
      end
    end

    document
  end

  def relative(path)
    path.relative_path_from(@root)
  rescue ArgumentError
    path
  end
end

files = [ENTRY, *ROOT.glob('contracts/openapi/**/*.yaml')].uniq.sort

duplicate_keys = files.flat_map { |path| DuplicateKeyScanner.new(ROOT).scan(path) }
unless duplicate_keys.empty?
  duplicate_keys.each do |finding|
    warn "Duplicate OpenAPI mapping key: #{finding[:path].relative_path_from(ROOT)}:#{finding[:line]} " \
         "key #{finding[:key].inspect} (key path #{finding[:key_path]}); first declared at line #{finding[:first_line]}"
  end
  abort "OpenAPI duplicate mapping keys: #{duplicate_keys.length}. " \
        'A repeated key silently discards the earlier value (RV-11 class); merge or split the mapping instead.'
end

validator = RefValidator.new(ROOT)
files.each { |path| validator.validate_file(path) }

puts "OpenAPI references OK: checked #{validator.checked_refs} refs across #{files.length} files."

# --- 第二遍：OpenAPI ↔ Core 路由目录绑定 ---------------------------------------
#
# x-sforum-route-id 是契约与 Core 路由目录之间唯一的指针。写错一个字母、把 GET 标到
# PUT 上、或让 path 与目录不一致，OpenAPI 本身仍然「语法正确」，但客户端读到的权限
# 说明就与运行时守卫对不上。这里把四件事钉在一起：目录里存在该 id、method 逐字一致、
# path 一致（目录的 /api/v1 前缀与 :param 写法都要归一）、声明的权限键都在权限目录里。
ROUTE_CATALOG = ROOT.join('docs/extensions/v3/catalogs/routes.json')
PERMISSION_SEEDS = ROOT.join('apps/api/app/Models/Identity/seeds.go')

# 必须被标注的十条路由（PRD2-01/02/03 的交付面）。
REQUIRED_ROUTE_IDS = %w[
  core.route.forum.admin_categories
  core.route.forum.admin_category_groups
  core.route.forum.admin_tags
  core.route.forum.admin_content_topics
  core.route.forum.admin_content_topic
  core.route.forum.admin_content_comments
  core.route.forum.admin_content_comment
  core.route.attachments.get_user_upload_policy
  core.route.attachments.set_user_upload_policy
  core.route.attachments.delete_user_upload_policy
].freeze

HTTP_OPERATIONS = %w[get post put patch delete head options].freeze

abort "Route catalog is missing: #{ROUTE_CATALOG.relative_path_from(ROOT)}" unless ROUTE_CATALOG.file?
abort "Permission seeds are missing: #{PERMISSION_SEEDS.relative_path_from(ROOT)}" unless PERMISSION_SEEDS.file?

catalog = JSON.parse(ROUTE_CATALOG.read).to_h { |route| [route['id'], route] }
seeded_permissions = PERMISSION_SEEDS.read.scan(/Permission[A-Za-z0-9]+\s*=\s*"([a-z][a-z0-9._-]*)"/).flatten.to_set

# 目录路径 → 契约路径：去掉 /api/v1 前缀，:param → {param}。
def contract_path_for(catalog_path)
  catalog_path.delete_prefix('/api/v1').gsub(/:([A-Za-z0-9_]+)/, '{\1}')
end

entry_document = YAML.load_file(ENTRY.to_s)
binding_resolver = RefValidator.new(ROOT)
annotated = 0
seen_route_ids = Hash.new(0)

# 先把每个路径项下的操作摊平：path item 本身也可以是 $ref。
resolved_operations = []
(entry_document['paths'] || {}).each do |contract_path, item|
  if item.is_a?(Hash) && item['$ref'].is_a?(String)
    item = binding_resolver.resolve_reference(ENTRY, item['$ref'])
  end
  next unless item.is_a?(Hash)

  item.each do |operation_key, operation|
    next unless HTTP_OPERATIONS.include?(operation_key.to_s)
    next unless operation.is_a?(Hash)

    resolved_operations << [contract_path, operation_key.to_s.upcase, operation]
  end
end

# 重复 route-id 必须先查：一条目录路由只能指向一个契约操作。放在逐条校验之前，
# 是因为重复本身是结构错误——两条标注里只要有一条路径写错，逐条校验会先报路径，
# 把「同一个 id 出现两次」这个更根本的事实盖掉。
route_id_locations = Hash.new { |hash, key| hash[key] = [] }
resolved_operations.each do |contract_path, http_method, operation|
  route_id = operation['x-sforum-route-id']
  next if route_id.nil?

  route_id_locations[route_id] << "#{http_method} #{contract_path}"
end

duplicate_route_ids = route_id_locations.select { |_route_id, locations| locations.length > 1 }
unless duplicate_route_ids.empty?
  duplicate_route_ids.each do |route_id, locations|
    warn "Duplicate x-sforum-route-id: #{route_id} annotated #{locations.length} times (#{locations.join('; ')})"
  end
  abort 'Duplicate x-sforum-route-id annotations: one catalog route must map to exactly one contract operation.'
end

resolved_operations.each do |contract_path, http_method, operation|
  route_id = operation['x-sforum-route-id']
  next if route_id.nil?

  annotated += 1
  seen_route_ids[route_id] += 1

  route = catalog[route_id]
  if route.nil?
    abort "x-sforum-route-id #{route_id} on #{http_method} #{contract_path} is not declared in docs/extensions/v3/catalogs/routes.json"
  end

  unless route['method'] == http_method
    abort "x-sforum-route-id #{route_id} is annotated on #{http_method} #{contract_path}, " \
          "but the route catalog declares #{route['method']} #{contract_path_for(route['path'])}"
  end

  expected_path = contract_path_for(route['path'])
  unless expected_path == contract_path
    abort "x-sforum-route-id #{route_id} is annotated on path #{contract_path}, " \
          "but the route catalog path is #{expected_path}"
  end

  Array(route.dig('guard', 'permissions')).each do |permission|
    next if seeded_permissions.include?(permission)

    abort "x-sforum-route-id #{route_id} declares permission #{permission} " \
          "that does not exist in apps/api/app/Models/Identity/seeds.go"
  end
end

missing_route_ids = REQUIRED_ROUTE_IDS.reject { |id| seen_route_ids.key?(id) }
unless missing_route_ids.empty?
  abort "Missing x-sforum-route-id annotations for: #{missing_route_ids.join(', ')}"
end

puts "OpenAPI route bindings OK: #{annotated} annotated operations, #{REQUIRED_ROUTE_IDS.length} required ids."
