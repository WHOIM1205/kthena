/*
Copyright The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gie

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/volcano-sh/kthena/test/e2e/framework"
	routercontext "github.com/volcano-sh/kthena/test/e2e/router/context"
	"github.com/volcano-sh/kthena/test/e2e/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	testCtx         *routercontext.RouterTestContext
	testNamespace   string
	kthenaNamespace string
)

func TestMain(m *testing.M) {
	testNamespace = "kthena-e2e-gie-" + utils.RandomString(5)

	config := framework.NewDefaultConfig()
	kthenaNamespace = config.Namespace
	config.NetworkingEnabled = true
	config.GatewayAPIEnabled = true
	config.InferenceExtensionEnabled = true

	if err := framework.InstallKthena(config); err != nil {
		fmt.Printf("Failed to install kthena: %v\n", err)
		os.Exit(1)
	}

	var err error
	testCtx, err = routercontext.NewRouterTestContext(testNamespace)
	if err != nil {
		fmt.Printf("Failed to create router test context: %v\n", err)
		_ = framework.UninstallKthena(config.Namespace)
		os.Exit(1)
	}

	if err := testCtx.CreateTestNamespace(); err != nil {
		fmt.Printf("Failed to create test namespace: %v\n", err)
		_ = framework.UninstallKthena(config.Namespace)
		os.Exit(1)
	}

	if err := testCtx.SetupCommonComponents(); err != nil {
		fmt.Printf("Failed to setup common components: %v\n", err)
		_ = testCtx.DeleteTestNamespace()
		_ = framework.UninstallKthena(config.Namespace)
		os.Exit(1)
	}

	code := m.Run()

	if err := testCtx.CleanupCommonComponents(); err != nil {
		fmt.Printf("Failed to cleanup common components: %v\n", err)
	}

	if err := testCtx.DeleteTestNamespace(); err != nil {
		fmt.Printf("Failed to delete test namespace: %v\n", err)
	}

	if err := framework.UninstallKthena(config.Namespace); err != nil {
		fmt.Printf("Failed to uninstall kthena: %v\n", err)
	}

	os.Exit(code)
}

func TestGatewayInferenceExtension(t *testing.T) {
	ctx := context.Background()

	// 1. Deploy InferencePool
	t.Log("Deploying InferencePool...")
	inferencePoolGVR := schema.GroupVersionResource{
		Group:    "inference.networking.k8s.io",
		Version:  "v1",
		Resource: "inferencepools",
	}

	inferencePoolRaw := utils.LoadYAMLFromFile[unstructured.Unstructured]("examples/kthena-router/InferencePool.yaml")
	inferencePoolRaw.SetNamespace(testNamespace)

	_, err := testCtx.DynamicClient.Resource(inferencePoolGVR).Namespace(testNamespace).Create(ctx, inferencePoolRaw, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create InferencePool")

	t.Cleanup(func() {
		_ = testCtx.DynamicClient.Resource(inferencePoolGVR).Namespace(testNamespace).Delete(context.Background(), inferencePoolRaw.GetName(), metav1.DeleteOptions{})
	})

	// 2. Deploy HTTPRoute
	t.Log("Deploying HTTPRoute...")
	httpRouteGVR := schema.GroupVersionResource{
		Group:    "gateway.networking.k8s.io",
		Version:  "v1",
		Resource: "httproutes",
	}

	httpRouteRaw := utils.LoadYAMLFromFile[unstructured.Unstructured]("examples/kthena-router/HTTPRoute.yaml")
	httpRouteRaw.SetNamespace(testNamespace)

	// Update parentRefs in unstructured to point to the kthena installation namespace
	parentRefs, found, err := unstructured.NestedSlice(httpRouteRaw.Object, "spec", "parentRefs")
	if err == nil && found {
		for i := range parentRefs {
			if ref, ok := parentRefs[i].(map[string]interface{}); ok {
				ref["namespace"] = kthenaNamespace
			}
		}
		_ = unstructured.SetNestedSlice(httpRouteRaw.Object, parentRefs, "spec", "parentRefs")
	}

	_, err = testCtx.DynamicClient.Resource(httpRouteGVR).Namespace(testNamespace).Create(ctx, httpRouteRaw, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create HTTPRoute")

	t.Cleanup(func() {
		_ = testCtx.DynamicClient.Resource(httpRouteGVR).Namespace(testNamespace).Delete(context.Background(), httpRouteRaw.GetName(), metav1.DeleteOptions{})
	})

	// 3. Test accessing the route
	t.Log("Testing chat completions via HTTPRoute and InferencePool...")
	messages := []utils.ChatMessage{
		utils.NewChatMessage("user", "Hello GIE"),
	}

	// The model name in HTTPRoute.yaml is not explicitly defined in the route itself usually,
	// but kthena-router matches by model name if it's a /v1/chat/completions request.
	// In HTTPRoute.yaml, it matches path / and sends to InferencePool deepseek-r1-1-5b.
	// If the request path is /v1/chat/completions, and the route matches /, it will be used.

	// However, we need to know what model name to use.
	// The InferencePool name is deepseek-r1-1-5b.
	// The mock deployments also use this name.
	utils.CheckChatCompletions(t, "deepseek-r1-1-5b", messages)
}
