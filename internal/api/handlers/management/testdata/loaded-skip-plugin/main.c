#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static const char registration_response[] =
	"{\"ok\":true,\"result\":{"
	"\"schema_version\":1,"
	"\"metadata\":{"
	"\"Name\":\"Loaded Fixture\","
	"\"Version\":\"1.0.0\","
	"\"Author\":\"test\","
	"\"GitHubRepository\":\"https://example.com/loaded-fixture\""
	"},"
	"\"capabilities\":{\"request_translator\":true}"
	"}}";

static int loaded_plugin_call(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	(void)request;
	(void)request_len;
	if (!response) {
		return 1;
	}
	response->ptr = NULL;
	response->len = 0;
	const char* body = "loaded";
	if (method && strcmp(method, "plugin.register") == 0) {
		body = registration_response;
	}
	size_t length = strlen(body);
	void* copy = malloc(length ? length : 1);
	if (!copy) {
		return 1;
	}
	memcpy(copy, body, length);
	response->ptr = copy;
	response->len = length;
	return 0;
}

static void loaded_plugin_free(void* ptr, size_t length) {
	(void)length;
	free(ptr);
}

static void loaded_plugin_shutdown(void) {
}

__attribute__((visibility("default"))) int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	(void)host;
	if (!plugin) {
		return 1;
	}
	plugin->abi_version = 1;
	plugin->call = loaded_plugin_call;
	plugin->free_buffer = loaded_plugin_free;
	plugin->shutdown = loaded_plugin_shutdown;
	return 0;
}
