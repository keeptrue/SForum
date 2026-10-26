#!/usr/bin/env node
// 校验 Web SDK 包与脚手架/桥接契约一致，不打包、不发布。
//
// 这条断言原本只存在于 release.yml 的 publish-web-sdks 里，导致「SDK 版本抬了但
// Vue 脚手架模板没跟上」这类问题要等到发版最后一步才暴露（v3.0.17 就是这么红的）。
// 放进主门禁后，同一个错误在 PR/推 main 阶段就会被拦住。
import { readAndValidateSDKPackages } from './web-sdk-packages.mjs'

const packages = readAndValidateSDKPackages()
const summary = packages.map((entry) => `${entry.name}@${entry.manifest.version}`).join(', ')
console.log(`web SDK packages OK: ${summary}`)
