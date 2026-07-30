(() => {
  "use strict";

  const apiBase = "/api/admin/mcp-api-keys";
  const tokenStorageKey = "mcpAdminToken";
  let adminToken = sessionStorage.getItem(tokenStorageKey) || "";
  let items = [];
  let pendingAction = null;

  const byID = (id) => document.getElementById(id);
  const authDialog = byID("authDialog");
  const formDialog = byID("keyFormDialog");
  const confirmDialog = byID("confirmDialog");
  const secretDialog = byID("secretDialog");

  async function request(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${adminToken}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers, cache: "no-store" });
    if (response.status === 401 || response.status === 429) {
      if (response.status === 401) {
        adminToken = "";
        sessionStorage.removeItem(tokenStorageKey);
        showAuth("管理者 Token 無效或已變更。");
      }
    }
    if (!response.ok) {
      let message = `請求失敗（${response.status}）`;
      try {
        const payload = await response.json();
        message = payload.error?.message || message;
      } catch (_) {}
      throw new Error(message);
    }
    if (response.status === 204) return null;
    return response.json();
  }

  function showAuth(message = "") {
    byID("authError").hidden = !message;
    byID("authError").textContent = message;
    if (!authDialog.open) authDialog.showModal();
    queueMicrotask(() => byID("adminToken").focus());
  }

  async function loadKeys() {
    setView("loading");
    try {
      const payload = await request(apiBase);
      items = payload.items;
      renderRows();
      setView(items.length ? "table" : "empty");
      byID("countBadge").textContent = String(items.length);
      if (authDialog.open) authDialog.close();
    } catch (error) {
      if (!adminToken) return;
      byID("errorMessage").textContent = error.message;
      setView("error");
    }
  }

  function setView(name) {
    byID("loadingState").hidden = name !== "loading";
    byID("errorState").hidden = name !== "error";
    byID("emptyState").hidden = name !== "empty";
    byID("tableWrap").hidden = name !== "table";
  }

  function renderRows() {
    const body = byID("keyRows");
    body.replaceChildren();
    for (const item of items) {
      const row = document.createElement("tr");
      row.append(
        cellWithName(item),
        textCell(item.maskedKey, "Key", "masked"),
        badgeCell(item.status),
        textCell(formatDate(item.createdAt), "建立時間"),
        textCell(formatDate(item.lastUsedAt), "最後使用"),
        textCell(formatDate(item.expiresAt), "到期時間"),
        actionCell(item),
      );
      body.append(row);
    }
  }

  function cellWithName(item) {
    const cell = document.createElement("td");
    cell.dataset.label = "名稱";
    const strong = document.createElement("strong");
    strong.textContent = item.name;
    cell.append(strong);
    if (item.description) {
      const description = document.createElement("span");
      description.className = "description";
      description.textContent = item.description;
      cell.append(description);
    }
    return cell;
  }

  function textCell(value, label, className = "") {
    const cell = document.createElement("td");
    cell.dataset.label = label;
    cell.textContent = value || "—";
    if (className) cell.className = className;
    return cell;
  }

  function badgeCell(status) {
    const cell = document.createElement("td");
    cell.dataset.label = "狀態";
    const badge = document.createElement("span");
    badge.className = `badge ${status}`;
    const labels = { active: "Active", disabled: "Disabled", revoked: "Revoked", expired: "Expired" };
    badge.textContent = labels[status] || status;
    cell.append(badge);
    return cell;
  }

  function actionCell(item) {
    const cell = document.createElement("td");
    cell.className = "actions";
    cell.dataset.label = "操作";
    const group = document.createElement("div");
    group.className = "actions-buttons";
    group.append(actionButton("Edit", () => openEdit(item)));
    if (item.status === "active") {
      group.append(actionButton("Disable", () => confirmStatus(item, "disable")));
    } else if (item.status === "disabled") {
      group.append(actionButton("Enable", () => confirmStatus(item, "enable")));
    }
    group.append(actionButton("Rotate", () => confirmRotate(item)));
    group.append(actionButton("Delete", () => confirmDelete(item), true));
    cell.append(group);
    return cell;
  }

  function actionButton(label, action, danger = false) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `small ${danger ? "danger" : "secondary"}`;
    button.textContent = label;
    button.addEventListener("click", action);
    return button;
  }

  function formatDate(value) {
    if (!value) return "—";
    const date = new Date(value);
    const utcMs = date.getTime() + date.getTimezoneOffset() * 60000;
    const taipei = new Date(utcMs + 8 * 60 * 60000);
    const pad = (n) => String(n).padStart(2, "0");
    const y = taipei.getFullYear();
    const m = pad(taipei.getMonth() + 1);
    const d = pad(taipei.getDate());
    const hh = pad(taipei.getHours());
    const mm = pad(taipei.getMinutes());
    const ss = pad(taipei.getSeconds());
    return `${y}-${m}-${d} ${hh}:${mm}:${ss} +08:00`;
  }

  function openCreate() {
    byID("keyFormTitle").textContent = "建立 API Key";
    byID("editingID").value = "";
    byID("keyName").value = "";
    byID("keyDescription").value = "";
    byID("keyExpires").value = "";
    byID("formError").hidden = true;
    formDialog.showModal();
    byID("keyName").focus();
  }

  function openEdit(item) {
    byID("keyFormTitle").textContent = "編輯 API Key";
    byID("editingID").value = item.id;
    byID("keyName").value = item.name;
    byID("keyDescription").value = item.description;
    byID("keyExpires").value = item.expiresAt ? toLocalInput(item.expiresAt) : "";
    byID("formError").hidden = true;
    formDialog.showModal();
    byID("keyName").focus();
  }

  function toLocalInput(value) {
    const date = new Date(value);
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
  }

  function expirationValue() {
    const value = byID("keyExpires").value;
    return value ? new Date(value).toISOString() : null;
  }

  async function submitKey(event) {
    event.preventDefault();
    const id = byID("editingID").value;
    const current = items.find((item) => item.id === id);
    const body = {
      name: byID("keyName").value.trim(),
      description: byID("keyDescription").value.trim(),
      expiresAt: expirationValue(),
    };
    if (current) body.version = current.version;
    byID("saveKeyButton").disabled = true;
    try {
      const payload = await request(id ? `${apiBase}/${id}` : apiBase, {
        method: id ? "PATCH" : "POST",
        body: JSON.stringify(body),
      });
      formDialog.close();
      if (payload.apiKey) showSecret(payload.apiKey, payload.notice, false);
      showNotice(id ? "API Key 已更新。" : "API Key 已建立。");
      await loadKeys();
    } catch (error) {
      byID("formError").textContent = error.message;
      byID("formError").hidden = false;
    } finally {
      byID("saveKeyButton").disabled = false;
    }
  }

  function openConfirm(title, message, label, action, danger = false) {
    byID("confirmTitle").textContent = title;
    byID("confirmMessage").textContent = message;
    byID("confirmError").hidden = true;
    byID("confirmButton").textContent = label;
    byID("confirmButton").className = danger ? "danger" : "";
    pendingAction = action;
    confirmDialog.showModal();
    byID("confirmButton").focus();
  }

  function confirmStatus(item, action) {
    const disabling = action === "disable";
    openConfirm(
      disabling ? "停用 API Key" : "啟用 API Key",
      `${disabling ? "停用" : "啟用"}「${item.name}」(${item.maskedKey})？${disabling ? "停用後，後續 MCP 請求將立即失敗。" : ""}`,
      disabling ? "確認停用" : "確認啟用",
      async () => request(`${apiBase}/${item.id}/${action}`, {
        method: "POST", body: JSON.stringify({ version: item.version }),
      }),
      disabling,
    );
  }

  function confirmRotate(item) {
    openConfirm(
      "輪替 API Key",
      `輪替「${item.name}」後，舊 API Key 將立即失效。使用舊 Key 的 MCP Client 必須更新設定。`,
      "確認輪替",
      async () => {
        const payload = await request(`${apiBase}/${item.id}/rotate`, {
          method: "POST", body: JSON.stringify({ version: item.version }),
        });
        showSecret(payload.apiKey, payload.notice, true);
      },
      true,
    );
  }

  function confirmDelete(item) {
    openConfirm(
      "刪除 API Key",
      `確定刪除「${item.name}」(${item.maskedKey})？刪除後立即失效且無法復原。`,
      "永久刪除",
      async () => request(`${apiBase}/${item.id}`, {
        method: "DELETE", body: JSON.stringify({ version: item.version }),
      }),
      true,
    );
  }

  async function runConfirmed(event) {
    event.preventDefault();
    if (!pendingAction) return;
    byID("confirmButton").disabled = true;
    try {
      await pendingAction();
      pendingAction = null;
      confirmDialog.close();
      showNotice("操作已完成。");
      await loadKeys();
    } catch (error) {
      byID("confirmError").textContent = error.message;
      byID("confirmError").hidden = false;
    } finally {
      byID("confirmButton").disabled = false;
    }
  }

  function showSecret(fullKey, notice, rotated) {
    byID("secretTitle").textContent = rotated ? "API Key 已輪替" : "API Key 已建立";
    byID("secretNotice").textContent = notice;
    byID("oneTimeKey").value = fullKey;
    byID("copyStatus").textContent = "";
    if (!secretDialog.open) secretDialog.showModal();
    byID("copyButton").focus();
  }

  async function copySecret() {
    const input = byID("oneTimeKey");
    try {
      await navigator.clipboard.writeText(input.value);
      byID("copyStatus").textContent = "已複製到剪貼簿。";
    } catch (_) {
      input.select();
      byID("copyStatus").textContent = "請按 Ctrl+C 或 ⌘C 複製。";
    }
  }

  function closeSecret() {
    byID("oneTimeKey").value = "";
    byID("copyStatus").textContent = "";
    secretDialog.close();
  }

  function showNotice(message) {
    const notice = byID("notice");
    notice.textContent = message;
    notice.hidden = false;
    setTimeout(() => { notice.hidden = true; }, 4000);
  }

  byID("authForm").addEventListener("submit", async (event) => {
    event.preventDefault();
    adminToken = byID("adminToken").value;
    sessionStorage.setItem(tokenStorageKey, adminToken);
    byID("adminToken").value = "";
    await loadKeys();
  });
  byID("refreshButton").addEventListener("click", loadKeys);
  byID("retryButton").addEventListener("click", loadKeys);
  byID("createButton").addEventListener("click", openCreate);
  byID("keyForm").addEventListener("submit", submitKey);
  byID("cancelFormButton").addEventListener("click", () => formDialog.close());
  byID("confirmButton").addEventListener("click", runConfirmed);
  byID("cancelConfirmButton").addEventListener("click", () => { pendingAction = null; });
  byID("copyButton").addEventListener("click", copySecret);
  byID("closeSecretButton").addEventListener("click", closeSecret);
  secretDialog.addEventListener("cancel", (event) => { event.preventDefault(); closeSecret(); });

  if (adminToken) {
    loadKeys();
  } else {
    showAuth();
  }
})();
