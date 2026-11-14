#!/usr/bin/env ruby
# frozen_string_literal: true

require 'json'
require 'pathname'
require 'set'
require 'yaml'

ROOT = Pathname.new(__dir__).join('..').expand_path
ENTRY = ROOT.join('contracts/openapi.yaml')

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

(entry_document['paths'] || {}).each do |contract_path, item|
  if item.is_a?(Hash) && item['$ref'].is_a?(String)
    item = binding_resolver.resolve_reference(ENTRY, item['$ref'])
  end
  next unless item.is_a?(Hash)

  item.each do |operation_key, operation|
    next unless HTTP_OPERATIONS.include?(operation_key.to_s)
    next unless operation.is_a?(Hash)

    route_id = operation['x-sforum-route-id']
    next if route_id.nil?

    http_method = operation_key.to_s.upcase
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
end

missing_route_ids = REQUIRED_ROUTE_IDS.reject { |id| seen_route_ids.key?(id) }
unless missing_route_ids.empty?
  abort "Missing x-sforum-route-id annotations for: #{missing_route_ids.join(', ')}"
end

puts "OpenAPI route bindings OK: #{annotated} annotated operations, #{REQUIRED_ROUTE_IDS.length} required ids."
