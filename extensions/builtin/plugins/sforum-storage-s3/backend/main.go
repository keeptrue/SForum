package main

import (
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	provider := pluginsdk.NewMultiStorageProvider("storage.s3", newS3Backend)
	pluginv2.Serve(pluginsdk.NewStorageProviderV2(provider))
}
