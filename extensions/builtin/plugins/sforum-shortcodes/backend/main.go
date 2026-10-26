package main

import (
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	plugin := &shortcodePlugin{server: pluginv2.NewServer()}
	registry, err := pluginv2.NewContentRegistry(
		plugin.contentDefinition(shortcodeUserID, shortcodeUserVersion, shortcodeUserSchema),
		plugin.contentDefinition(shortcodeTopicID, shortcodeTopicVersion, shortcodeTopicSchema),
		plugin.contentDefinition(shortcodeCommentID, shortcodeCommentVersion, shortcodeCommentSchema),
		plugin.contentDefinition(shortcodeCategoryID, shortcodeCategoryVersion, shortcodeCategorySchema),
		plugin.contentDefinition(shortcodeFriendLinksID, shortcodeFriendLinksVer, shortcodeFriendLinksSchema),
		plugin.contentDefinition(shortcodeLoginID, shortcodeLoginVersion, shortcodeLoginSchema),
		plugin.contentDefinition(shortcodeReplyID, shortcodeReplyVersion, shortcodeReplySchema),
		plugin.contentDefinition(shortcodeOnlyAuthorID, shortcodeOnlyAuthorVersion, shortcodeOnlyAuthorSchema),
	)
	if err != nil {
		panic(err)
	}
	// 只协商 content.runtime@1；不声明其它 Host 权威能力。
	pluginv2.Serve(plugin.server.
		WithFeatures(pluginv2.ContentRuntimeProtocolFeature()).
		WithContentRegistry(registry))
}
