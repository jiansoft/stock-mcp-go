// stock-mcp 的 Swagger UI 初始化。獨立成檔而不寫在 HTML 裡，是因為頁面的 CSP 不允許 inline script。
(function () {
    "use strict";

    // 頁面可能在 /docs 或 /docs/，也可能掛在反向代理的子路徑下；OpenAPI 文件固定在同一層的 /openapi.json。
    var base = window.location.pathname.replace(/\/docs\/?$/, "");

    window.addEventListener("load", function () {
        window.ui = window.SwaggerUIBundle({
            url: base + "/openapi.json",
            dom_id: "#swagger-ui",
            deepLinking: true,
            defaultModelsExpandDepth: 0,
            // MCP Streamable HTTP 規定 POST 的 Accept 必須同時包含 application/json 與
            // text/event-stream，否則伺服器回 400；Swagger UI 只會帶單一型別，這裡補齊。
            requestInterceptor: function (request) {
                if (request.method === "POST" && typeof request.body === "string" && request.body.indexOf("\"jsonrpc\"") >= 0) {
                    request.headers.Accept = "application/json, text/event-stream";
                }
                return request;
            }
        });
    });
})();
