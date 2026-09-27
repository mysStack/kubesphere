/*
 * Copyright 2024 the KubeSphere Authors.
 * Please refer to the LICENSE file in the root directory of the project.
 * https://github.com/kubesphere/kubesphere/blob/master/LICENSE
 */

package options

import (
	"testing"

	"kubesphere.io/kubesphere/pkg/config"
	controlleroptions "kubesphere.io/kubesphere/pkg/controller/options"
)

func TestMergePropagatesExtensionOptions(t *testing.T) {
	serverOptions := NewAPIServerOptions()
	serverOptions.Merge(&config.Config{
		ExtensionOptions: &controlleroptions.ExtensionOptions{
			IgnoreCompatibilityVersion: true,
		},
	})

	if serverOptions.ExtensionOptions == nil {
		t.Fatal("expected extension options to be propagated to the API server")
	}
	if !serverOptions.ExtensionOptions.IgnoreCompatibilityVersion {
		t.Fatal("expected compatibility version checks to be disabled")
	}
}
