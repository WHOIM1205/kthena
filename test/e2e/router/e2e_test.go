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

package router

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/volcano-sh/kthena/test/e2e/framework"
	routercontext "github.com/volcano-sh/kthena/test/e2e/router/context"
	"github.com/volcano-sh/kthena/test/e2e/utils"
)

var (
	testCtx         *routercontext.RouterTestContext
	testNamespace   string
	kthenaNamespace string
)

// TestMain runs setup and cleanup for all tests in this package.
func TestMain(m *testing.M) {
	testNamespace = "kthena-e2e-router-" + utils.RandomString(5)

	config := framework.NewDefaultConfig()
	kthenaNamespace = config.Namespace
	// Router tests need networking enabled
	config.NetworkingEnabled = true

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

	// Create test namespace
	if err := testCtx.CreateTestNamespace(); err != nil {
		fmt.Printf("Failed to create test namespace: %v\n", err)
		_ = framework.UninstallKthena(config.Namespace)
		os.Exit(1)
	}

	// Setup common components
	if err := testCtx.SetupCommonComponents(); err != nil {
		fmt.Printf("Failed to setup common components: %v\n", err)
		_ = testCtx.DeleteTestNamespace()
		_ = framework.UninstallKthena(config.Namespace)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// Cleanup common components
	if err := testCtx.CleanupCommonComponents(); err != nil {
		fmt.Printf("Failed to cleanup common components: %v\n", err)
	}

	// Delete test namespace
	if err := testCtx.DeleteTestNamespace(); err != nil {
		fmt.Printf("Failed to delete test namespace: %v\n", err)
	}

	if err := framework.UninstallKthena(config.Namespace); err != nil {
		fmt.Printf("Failed to uninstall kthena: %v\n", err)
	}

	os.Exit(code)
}

// NOTE: Most test cases in this package should follow the same pattern as the existing cases:
// 1. Implement the test logic in shared.go as a Shared function (e.g., TestModelRouteSimpleShared)
// 2. Call the shared function from router/e2e_test.go with useGatewayAPI=false (no ParentRefs)
// 3. Call the shared function from gateway-api/e2e_test.go with useGatewayAPI=true (with ParentRefs to default Gateway)
//
// This pattern allows code reuse and ensures that tests work both with and without Gateway API.
// Only Gateway API-specific tests (like TestDuplicateModelName)
// should be implemented directly in gateway-api/e2e_test.go without sharing.

// TestModelRouteSimple tests a simple ModelRoute deployment and access.
// This test runs the shared test function without Gateway API (no ParentRefs).
func TestModelRouteSimple(t *testing.T) {
	TestModelRouteSimpleShared(t, testCtx, testNamespace, false, "")
}

// TestModelRouteMultiModels tests ModelRoute with multiple models.
// This test runs the shared test function without Gateway API (no ParentRefs).
func TestModelRouteMultiModels(t *testing.T) {
	TestModelRouteMultiModelsShared(t, testCtx, testNamespace, false, "")
}

// TestModelRoutePrefillDecodeDisaggregation tests PD disaggregation with ModelServing, ModelServer, and ModelRoute.
// This test runs the shared test function without Gateway API (no ParentRefs).
func TestModelRoutePrefillDecodeDisaggregation(t *testing.T) {
	TestModelRoutePrefillDecodeDisaggregationShared(t, testCtx, testNamespace, false, "")
}

// TestModelRouteSubset tests ModelRoute with subset routing.
// This test runs the shared test function without Gateway API (no ParentRefs).
func TestModelRouteSubset(t *testing.T) {
	TestModelRouteSubsetShared(t, testCtx, testNamespace, false, "")
}

// test for modelroute with rate limit
func TestModelRouteWithRateLimit(t *testing.T) {
	const (
		rateLimitWindowSeconds = 60
		windowResetBuffer      = 10 * time.Second
		inputTokenLimit        = 30
		outputTokenLimit       = 100
		tokensPerRequest       = 10
	)

	ctx := context.Background()

	//  Deploy ModelRoute with rate limiting configuration
	t.Log("Deploying ModelRoute with rate limiting configuration...")
	modelRoute := utils.LoadYAMLFromFile[networkingv1alpha1.ModelRoute]("examples/kthena-router/ModelRouteWithRateLimit.yaml")
	modelRoute.Namespace = testNamespace

	createdModelRoute, err := testCtx.KthenaClient.NetworkingV1alpha1().ModelRoutes(testNamespace).Create(ctx, modelRoute, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create ModelRoute")
	t.Logf("Successfully created ModelRoute: %s/%s", createdModelRoute.Namespace, createdModelRoute.Name)

	// Cleanup: Ensure ModelRoute is deleted after test completion
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		t.Logf("Cleaning up ModelRoute: %s/%s", createdModelRoute.Namespace, createdModelRoute.Name)
		if err := testCtx.KthenaClient.NetworkingV1alpha1().ModelRoutes(testNamespace).Delete(cleanupCtx, createdModelRoute.Name, metav1.DeleteOptions{}); err != nil {
			t.Logf("Warning: Failed to delete ModelRoute: %v", err)
		}
	})

	// Wait for ModelRoute to be ready
	t.Log("Waiting for ModelRoute to be ready...")
	require.Eventually(t, func() bool {
		mr, err := testCtx.KthenaClient.NetworkingV1alpha1().ModelRoutes(testNamespace).Get(ctx, createdModelRoute.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return mr != nil
	}, 2*time.Minute, 2*time.Second, "ModelRoute should be created")
	t.Log("ModelRoute created, waiting for rate limit window to be fresh...")

	// Wait for a full rate limit window to ensure we start with a clean slate
	time.Sleep((rateLimitWindowSeconds * time.Second) + windowResetBuffer)

	standardMessage := []utils.ChatMessage{
		utils.NewChatMessage("user", "hello world"),
	}

	// Test 1: Verify input token rate limit enforcement (30 tokens/minute)
	t.Run("VerifyInputTokenRateLimitEnforcement", func(t *testing.T) {
		t.Log("Test 1: Verifying input token rate limit")

		// Calculate expected successful requests
		expectedSuccessfulRequests := inputTokenLimit / tokensPerRequest
		if expectedSuccessfulRequests == 0 {
			t.Fatalf("Invalid test configuration: inputTokenLimit (%d) / tokensPerRequest (%d) = 0",
				inputTokenLimit, tokensPerRequest)
		}

		// Send requests until we exhaust the quota
		for i := 0; i < expectedSuccessfulRequests; i++ {
			resp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
			responseBody, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()

			require.NoError(t, readErr, "Failed to read response body on request %d", i+1)
			require.Equal(t, http.StatusOK, resp.StatusCode,
				"Request %d should succeed (consumed ~%d/%d tokens). Response: %s",
				i+1, (i+1)*tokensPerRequest, inputTokenLimit, string(responseBody))
			t.Logf("Request %d succeeded (consumed ~%d/%d tokens)", i+1, (i+1)*tokensPerRequest, inputTokenLimit)
		}

		// Next request should be rate limited (quota exhausted)
		rateLimitedResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		defer rateLimitedResp.Body.Close()

		assert.Equal(t, http.StatusTooManyRequests, rateLimitedResp.StatusCode,
			"Request %d should be rate limited", expectedSuccessfulRequests+1)

		errorBody, err := io.ReadAll(rateLimitedResp.Body)
		require.NoError(t, err, "Failed to read rate limit error response body")
		assert.Contains(t, strings.ToLower(string(errorBody)), "rate limit",
			"Rate limit error response must contain descriptive message")

		t.Logf("Input token rate limit enforced after %d requests", expectedSuccessfulRequests)
	})

	// Test 2 Verify rate limit window accuracy and persistence
	t.Run("VerifyRateLimitWindowAccuracy", func(t *testing.T) {
		t.Log("Test 2: Verifying rate limit window accuracy...")

		// Wait for window to reset from previous test
		windowResetDuration := (rateLimitWindowSeconds * time.Second) + windowResetBuffer
		t.Logf("Waiting %v for rate limit window reset from previous test...", windowResetDuration)
		time.Sleep(windowResetDuration)

		// Exhaust quota again to ensure rate limit is active
		expectedSuccessfulRequests := inputTokenLimit / tokensPerRequest
		for i := 0; i < expectedSuccessfulRequests; i++ {
			resp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
			resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "Request %d should succeed", i+1)
		}

		// Verify rate limit is active
		rateLimitedResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		rateLimitedResp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, rateLimitedResp.StatusCode,
			"Rate limit should be active after exhausting quota")

		const halfWindowDuration = 10 * time.Second
		t.Logf("Waiting %v (within rate limit window)...", halfWindowDuration)
		time.Sleep(halfWindowDuration)

		midWindowResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		midWindowResp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, midWindowResp.StatusCode,
			"Rate limit should persist within the time window")

		// Verify rate limit resets after window expiration (65 seconds > 60 seconds)
		remainingWindowDuration := (rateLimitWindowSeconds * time.Second) - halfWindowDuration + windowResetBuffer
		t.Logf("Waiting additional %v for window reset (total: %v)...",
			remainingWindowDuration, halfWindowDuration+remainingWindowDuration)
		time.Sleep(remainingWindowDuration)

		postWindowResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		postWindowResp.Body.Close()
		assert.Equal(t, http.StatusOK, postWindowResp.StatusCode,
			"Request should succeed after rate limit window expires")

		t.Log(" Rate limit window accuracy verified")
	})

	// Test 3: Verify rate limit reset mechanism
	t.Run("VerifyRateLimitResetMechanism", func(t *testing.T) {
		t.Log("Test 3: Verifying rate limit reset mechanism...")

		// Wait for window to reset from previous test
		windowResetDuration := (rateLimitWindowSeconds * time.Second) + windowResetBuffer
		t.Logf("Waiting %v for rate limit window reset from previous test...", windowResetDuration)
		time.Sleep(windowResetDuration)

		// Consume the quota again
		expectedSuccessfulRequests := inputTokenLimit / tokensPerRequest
		for i := 0; i < expectedSuccessfulRequests; i++ {
			resp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
			resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"Request %d should succeed", i+1)
		}

		// Confirm rate limiting is active
		preResetResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		preResetResp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, preResetResp.StatusCode,
			"Rate limit should be active before window reset")

		// Wait for complete window reset
		windowResetDuration = (rateLimitWindowSeconds * time.Second) + windowResetBuffer
		t.Logf("Waiting %v for complete rate limit window reset...", windowResetDuration)
		time.Sleep(windowResetDuration)

		// Verify quota is restored after reset (should allow 2 requests again)
		for i := 0; i < expectedSuccessfulRequests; i++ {
			resp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
			resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"Request %d should succeed after reset", i+1)
		}

		// Verify rate limiting kicks in again after consuming quota
		postResetRateLimitedResp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, standardMessage)
		postResetRateLimitedResp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, postResetRateLimitedResp.StatusCode,
			"Rate limit should be active again after consuming quota")

		t.Logf("Rate limit reset mechanism verified (quota restored: %d requests)", expectedSuccessfulRequests)
	})

	// Test 4: Verify output token rate limit enforcement
	t.Run("VerifyOutputTokenRateLimitEnforcement", func(t *testing.T) {
		t.Log("Test 4: Verifying output token rate limit (100 tokens/minute)...")

		// Wait for rate limit window to reset
		windowResetDuration := (rateLimitWindowSeconds * time.Second) + windowResetBuffer
		t.Logf("Waiting %v for rate limit window reset...", windowResetDuration)
		time.Sleep(windowResetDuration)

		longerPrompt := []utils.ChatMessage{
			utils.NewChatMessage("user", "Write a detailed explanation of rate limiting"),
		}

		// Send requests until we hit the output token limit
		var successfulRequests int
		var totalResponseSize int

		for attempt := 0; attempt < 10; attempt++ {
			resp := utils.SendChatRequest(t, createdModelRoute.Spec.ModelName, longerPrompt)
			responseBody, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()

			require.NoError(t, readErr, "Failed to read response body")

			if resp.StatusCode == http.StatusOK {
				successfulRequests++
				totalResponseSize += len(responseBody)
				t.Logf("Request %d succeeded, response size: %d bytes (total: %d bytes)",
					attempt+1, len(responseBody), totalResponseSize)
			} else if resp.StatusCode == http.StatusTooManyRequests {
				t.Logf("Output rate limited after %d requests", successfulRequests)
				assert.Contains(t, strings.ToLower(string(responseBody)), "rate limit",
					"Output rate limit error should mention rate limit")
				break
			} else {
				t.Fatalf("Unexpected HTTP status code %d on attempt %d", resp.StatusCode, attempt+1)
			}
		}

		// Should hit output limit before exhausting all attempts
		assert.Greater(t, successfulRequests, 0,
			"Expected at least one successful request before output rate limiting")
		assert.Less(t, successfulRequests, 10,
			"Expected to hit output rate limit (100 tokens) before 10 requests")

		t.Logf(" Output token rate limit enforced after %d requests", successfulRequests)
	})
}
