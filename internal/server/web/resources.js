// Shared resource interactions, embedded inside the console's private scope.
var selectedAccounts = new Set(),
  selectedGoogle = new Set(),
  selectedMail = new Set();
var visibleAccountIDs = [],
  settingsDirty = false,
  drawerDirty = false,
  drawerBusy = false;
var keyPage = 0,
  keyPageSize = 20,
  latestLogs = [],
  accountRefreshVersion = 0,
  drawerGeneration = 0;
var drawer = $("#resourceDialog"),
  confirmBox = $("#confirmDialog");
var confirmResolve = null,
  drawerSubmit = null;

function actionButton(text, fn, cls) {
  return btn(text, cls || "", fn);
}
function resourceHeading(parent, title, subtitle, button) {
  var head = mk("div", "resource-heading"),
    titleBox = mk("div");
  titleBox.append(mk("h1", null, title));
  head.appendChild(titleBox);
  if (button) head.appendChild(button);
  parent.prepend(head);
  return head;
}
function inputField(id, label, value, type, required, help) {
  var wrap = mk("div"),
    lab = mk("label", null, label),
    input = mk("input");
  lab.htmlFor = id;
  input.id = id;
  input.type = type || "text";
  input.value = value || "";
  input.required = !!required;
  input.maxLength = type === "password" ? 8192 : 200;
  if (type === "password") input.autocomplete = "new-password";
  wrap.append(lab, input);
  if (help) input.title = help;
  $("#resourceFields").appendChild(wrap);
  return input;
}
function detailPairs(pairs) {
  var dl = mk("dl", "detail-grid");
  pairs.forEach(function (p) {
    dl.append(
      mk("dt", null, p[0]),
      mk(
        "dd",
        null,
        p[1] === undefined || p[1] === null || p[1] === "" ? "—" : String(p[1]),
      ),
    );
  });
  $("#resourceDetails").appendChild(dl);
}
function openResource(title, saveLabel, handler) {
  drawerGeneration++;
  $("#resourceTitle").textContent = title;
  $("#resourceDetails").textContent = "";
  $("#resourceFields").textContent = "";
  $("#resourceError").textContent = "";
  $("#resourceSave").textContent = saveLabel || "保存修改";
  $("#resourceSave").hidden = !handler;
  $("#resourceSave").disabled = false;
  drawerDirty = false;
  drawerBusy = false;
  drawerSubmit = handler;
  if (!drawer.open) drawer.showModal();
}
async function closeResource() {
  if (drawerBusy) return;
  if (
    drawerDirty &&
    !(await askConfirm(
      "放弃未保存的修改？",
      "关闭后，当前表单的修改将不会保存。",
      "放弃修改",
    ))
  )
    return;
  drawer.close();
}
drawer.addEventListener("close", function () {
  if (drawer.open) return;
  $("#resourceFields").textContent = "";
  $("#resourceDetails").textContent = "";
  drawerSubmit = null;
  drawerDirty = false;
});
drawer.addEventListener("cancel", function (e) {
  e.preventDefault();
  closeResource();
});
$("#resourceClose").onclick = closeResource;
$("#resourceFields").addEventListener("input", function () {
  drawerDirty = true;
});
$("#resourceForm").onsubmit = async function (e) {
  e.preventDefault();
  if (drawerBusy || !drawerSubmit) return;
  drawerBusy = true;
  $("#resourceSave").disabled = true;
  $("#resourceError").textContent = "";
  var generation = drawerGeneration,
    oldText = $("#resourceSave").textContent;
  $("#resourceSave").textContent = "正在保存…";
  var fields = Array.from(
    $("#resourceFields").querySelectorAll("input,select,textarea"),
  );
  fields.forEach(function (field) {
    field.disabled = true;
  });
  try {
    await drawerSubmit();
    if (generation === drawerGeneration) {
      drawerDirty = false;
      drawer.close();
    }
  } catch (err) {
    if (generation === drawerGeneration)
      $("#resourceError").textContent = err.message;
  } finally {
    fields.forEach(function (field) {
      field.disabled = false;
    });
    if (generation === drawerGeneration) {
      drawerBusy = false;
      $("#resourceSave").disabled = false;
      $("#resourceSave").textContent = oldText;
    }
  }
};
function askConfirm(title, message, accept) {
  // Native modal dialogs provide focus trapping, Escape and focus restoration.
  if (confirmResolve) return Promise.resolve(false);
  $("#confirmTitle").textContent = title;
  $("#confirmMessage").textContent = message;
  $("#confirmAccept").textContent = accept || "确认删除";
  confirmBox.showModal();
  $("#confirmCancel").focus();
  return new Promise(function (resolve) {
    confirmResolve = resolve;
  });
}
function finishConfirm(value) {
  var resolve = confirmResolve;
  confirmResolve = null;
  confirmBox.close();
  if (resolve) resolve(value);
}
$("#confirmAccept").onclick = function () {
  finishConfirm(true);
};
$("#confirmCancel").onclick = function () {
  finishConfirm(false);
};
confirmBox.addEventListener("cancel", function (e) {
  e.preventDefault();
  finishConfirm(false);
});

async function runAction(button, fn) {
  if (button.disabled) return;
  button.disabled = true;
  try {
    await fn();
  } catch (e) {
    toast(e.message, "err");
  } finally {
    button.disabled = false;
  }
}
function selectionCell(set, id, kind) {
  var td = cell(""),
    box = mk("input");
  box.type = "checkbox";
  box.checked = set.has(id);
  box.dataset["select" + kind] = id;
  box.setAttribute("aria-label", "选择 " + id);
  box.onchange = function () {
    if (box.checked) set.add(id);
    else set.delete(id);
    updateBulkBars();
  };
  td.appendChild(box);
  return td;
}
function updateBulkBars() {
  [
    ["acct", selectedAccounts],
    ["google", selectedGoogle],
    ["mail", selectedMail],
  ].forEach(function (item) {
    $("#" + item[0] + "Bulk").hidden = !item[1].size;
    $("#" + item[0] + "Selected").textContent = "已选 " + item[1].size + " 项";
  });
  var checked = visibleAccountIDs.filter(function (id) {
    return selectedAccounts.has(id);
  }).length;
  $("#acctSelectAll").checked =
    !!visibleAccountIDs.length && checked === visibleAccountIDs.length;
  $("#acctSelectAll").indeterminate =
    checked > 0 && checked < visibleAccountIDs.length;
}
function pruneSelection(set, ids) {
  var valid = new Set(ids);
  set.forEach(function (id) {
    if (!valid.has(id)) set.delete(id);
  });
}
async function refreshResources() {
  var version = ++accountRefreshVersion,
    data;
  try {
    data = await api("/admin/api/state");
  } catch (e) {
    if (signedIn)
      toast("更改已提交，但列表刷新失败，请点击刷新重试：" + e.message, "err");
    return;
  }
  if (!signedIn || version !== accountRefreshVersion) return;
  state.data = data;
  renderAccounts(data);
  renderKeys(data);
  renderOverview(data);
}
function accountState(a) {
  return !a.enabled
    ? "已停用"
    : { ok: "可用", error: "异常", unknown: "未探测" }[a.status] || "异常";
}
function accountBalance(a) {
  var v = a.theos && a.theos.balance;
  return v === undefined || v === null || String(v).trim() === ""
    ? NaN
    : Number(v);
}
function secretControl(kind, id, masked) {
  var generation = drawerGeneration,
    requestVersion = 0;
  var box = mk("div", "secret-control"),
    label = mk("strong", null, kind === "account" ? "上游 Key" : "API 密钥");
  var value = mk("code", "secret-value", masked || "••••••••");
  value.dataset.secretValue = kind;
  var visible = false;
  var show = actionButton("查看", async function () {
    if (visible) {
      requestVersion++;
      value.textContent = masked || "••••••••";
      visible = false;
      show.textContent = "查看";
      show.setAttribute("aria-expanded", "false");
      return;
    }
    var request = ++requestVersion;
    await runAction(show, async function () {
      var response = await api("/admin/api/secrets", {
        method: "POST",
        body: { kind: kind, id: id },
      });
      if (
        generation !== drawerGeneration ||
        !drawer.open ||
        !value.isConnected ||
        request !== requestVersion
      )
        return;
      value.textContent = response.key;
      visible = true;
      show.textContent = "隐藏";
      show.setAttribute("aria-expanded", "true");
    });
  });
  show.dataset.secretReveal = kind;
  show.setAttribute("aria-expanded", "false");
  var copy = actionButton("复制", async function () {
    await runAction(copy, async function () {
      var response = await api("/admin/api/secrets", {
        method: "POST",
        body: { kind: kind, id: id },
      });
      if (generation !== drawerGeneration || !drawer.open || !value.isConnected)
        return;
      try {
        await navigator.clipboard.writeText(response.key);
        toast("密钥已复制", "ok");
      } catch (e) {
        value.textContent = response.key;
        visible = true;
        show.textContent = "隐藏";
        show.setAttribute("aria-expanded", "true");
        toast("请选中密钥手动复制", "err");
      }
    });
  });
  copy.dataset.secretCopy = kind;
  box.append(label, show, copy, value);
  $("#resourceDetails").appendChild(box);
}

function accountDetails(a) {
  openResource("账号详情与编辑", "保存修改", async function () {
    await api("/admin/api/accounts", {
      method: "POST",
      body: {
        action: "update",
        id: a.id,
        name: $("#editName").value.trim(),
        group: $("#editGroup").value.trim() || "default",
      },
    });
    await refreshResources();
    toast("已保存账号资料", "ok");
  });
  $("#resourceDetails").appendChild(
    mk(
      "div",
      "resource-path",
      (a.email || a.name) +
        " → " +
        (a.source || "手动导入") +
        " → DeepAI 账号池",
    ),
  );
  detailPairs([
    ["邮箱", a.email],
    ["状态", accountState(a)],
    ["余额", theosText(a.theos)],
    ["额度查询时间", a.theos && fmtTime(a.theos.checked_at)],
    ["套餐", a.plan || "Free"],
    ["邮箱验证", a.email_verified ? "已验证" : "未验证"],
    ["最近使用", fmtTime(a.last_used)],
    ["失败次数", a.failures],
    ["最近错误", a.last_err],
    ["密钥", a.key],
  ]);
  secretControl("account", a.id, a.key);
  inputField("editName", "账号名称", a.name, "text", true);
  inputField(
    "editGroup",
    "所属分组",
    a.group || "default",
    "text",
    true,
    "修改后，该账号将用于对应分组的请求。",
  );
  $("#editName").focus();
}
function accountMenu(a) {
  openResource("管理账号 · " + (a.email || a.name), null, null);
  detailPairs([
    ["当前状态", accountState(a)],
    ["最近错误", a.last_err],
  ]);
  var bar = mk("div", "resource-toolbar");
  bar.append(
    actionButton("编辑资料", function () {
      accountDetails(a);
    }),
    actionButton("刷新额度", async function () {
      drawer.close();
      await checkin(a.id);
    }),
    actionButton(a.enabled ? "停用账号" : "启用账号", async function () {
      await runAction(this, async function () {
        await api("/admin/api/accounts", {
          method: "POST",
          body: { action: "update", id: a.id, enabled: !a.enabled },
        });
        drawer.close();
        await refreshResources();
        toast(a.enabled ? "账号已停用" : "账号已启用", "ok");
      });
    }),
  );
  if (a.failures)
    bar.appendChild(
      actionButton("清除错误记录", async function () {
        await batchAccounts("clear-errors", [a.id]);
        drawer.close();
      }),
    );
  bar.appendChild(
    actionButton(
      "删除账号",
      async function () {
        if (
          await askConfirm(
            "删除这个账号？",
            (a.email || a.name) +
              "\n将从网关账号池移除。不会删除上游账号，也不会删除邮箱资料。",
          )
        ) {
          drawer.close();
          await batchAccounts("remove", [a.id]);
        }
      },
      "danger",
    ),
  );
  $("#resourceDetails").appendChild(bar);
}
renderAccounts = function (d) {
  var accounts = d.accounts || [],
    tb = $("#accounts"),
    groupSelect = $("#acctGroupFilter"),
    group = groupSelect.value;
  pruneSelection(
    selectedAccounts,
    accounts.map(function (a) {
      return a.id;
    }),
  );
  groupSelect.textContent = "";
  groupSelect.appendChild(new Option("全部分组", ""));
  Array.from(
    new Set(
      accounts.map(function (a) {
        return a.group || "default";
      }),
    ),
  )
    .sort()
    .forEach(function (g) {
      groupSelect.appendChild(new Option(g, g));
    });
  groupSelect.value = group;
  if (groupSelect.selectedIndex < 0) {
    groupSelect.value = "";
    group = "";
  }
  var query = $("#acctSearch").value.trim().toLowerCase(),
    status = $("#acctStatusFilter").value,
    balance = $("#acctBalanceFilter").value,
    verified = $("#acctVerifyFilter").value;
  var all = accounts.filter(function (a) {
    var n = accountBalance(a);
    return (
      [a.email, a.name, a.username].some(function (v) {
        return String(v || "")
          .toLowerCase()
          .includes(query);
      }) &&
      (!group || (a.group || "default") === group) &&
      (!status ||
        (status === "disabled"
          ? !a.enabled
          : a.enabled && (a.status || "unknown") === status)) &&
      (!balance ||
        (balance === "unknown"
          ? !Number.isFinite(n)
          : balance === "positive"
            ? Number.isFinite(n) && n > 0
            : Number.isFinite(n) && n <= 0)) &&
      (!verified ||
        (verified === "verified" ? a.email_verified : !a.email_verified))
    );
  });
  var sort = $("#acctSort").value;
  if (sort === "balance")
    all.sort(function (a, b) {
      return (
        (Number.isFinite(accountBalance(b)) ? accountBalance(b) : -Infinity) -
        (Number.isFinite(accountBalance(a)) ? accountBalance(a) : -Infinity)
      );
    });
  if (sort === "recent")
    all.sort(function (a, b) {
      return (b.last_used || 0) - (a.last_used || 0);
    });
  if (sort === "name")
    all.sort(function (a, b) {
      return (a.email || a.name || "").localeCompare(b.email || b.name || "");
    });
  acctPage = Math.max(
    0,
    Math.min(acctPage, Math.ceil(all.length / acctPageSize) - 1),
  );
  var start = acctPage * acctPageSize,
    page = all.slice(start, start + acctPageSize);
  visibleAccountIDs = page.map(function (a) {
    return a.id;
  });
  tb.textContent = "";
  page.forEach(function (a) {
    var name = cell(""),
      link = actionButton(
        a.email || a.name || "未命名账号",
        function () {
          accountDetails(a);
        },
        "resource-name",
      );
    link.dataset.accountId = a.id;
    name.append(
      link,
      mk(
        "div",
        "resource-sub",
        a.email && a.name !== a.email ? a.name : a.source || "手动导入",
      ),
    );
    var statusCell = cell("", "state-column");
    statusCell.appendChild(
      pill(
        accountState(a),
        !a.enabled
          ? ""
          : a.status === "ok"
            ? "ok"
            : a.status === "unknown"
              ? "warn"
              : "err",
      ),
    );
    tb.appendChild(
      row([
        selectionCell(selectedAccounts, a.id, "Account"),
        name,
        cell(a.group || "default", "secondary"),
        cell(theosText(a.theos), "numeric balance-column"),
        statusCell,
        cell(fmtTime(a.last_used), "secondary"),
        (function () {
          var c = cell("");
          c.appendChild(
            actionButton("更多", function () {
              accountMenu(a);
            }),
          );
          return c;
        })(),
      ]),
    );
  });
  if (!page.length)
    tb.appendChild(
      emptyRow(
        7,
        accounts.length
          ? "没有符合筛选条件的账号，请调整筛选。"
          : "暂无账号，点击右上角「添加账号」开始。",
      ),
    );
  updateAcctPager(all.length, Math.min(start + page.length, all.length));
  updateBulkBars();
  var counts = $("#acctCounts");
  counts.textContent = "";
  [
    ["全部", accounts.length, ""],
    [
      "可用",
      accounts.filter(function (a) {
        return a.enabled && a.status === "ok";
      }).length,
      "ok",
    ],
    [
      "异常",
      accounts.filter(function (a) {
        return a.enabled && a.status === "error";
      }).length,
      "error",
    ],
    [
      "已停用",
      accounts.filter(function (a) {
        return !a.enabled;
      }).length,
      "disabled",
    ],
  ].forEach(function (c) {
    var b = actionButton(c[0] + " " + c[1], function () {
      $("#acctStatusFilter").value = c[2];
      acctPage = 0;
      renderAccounts(state.data);
    });
    b.setAttribute("aria-pressed", status === c[2]);
    counts.appendChild(b);
  });
};
async function batchAccounts(operation, ids, group) {
  try {
    var r = await api("/admin/api/accounts", {
      method: "POST",
      body: { action: "batch", ids: ids, operation: operation, group: group },
    });
    (r.results || []).forEach(function (x) {
      if (x.ok) selectedAccounts.delete(x.id);
    });
    await refreshResources();
    showBatchResult(
      r,
      {
        enable: "启用",
        disable: "停用",
        remove: "删除",
        "set-group": "修改分组",
        "clear-errors": "清除错误记录",
      }[operation],
    );
  } catch (e) {
    toast("操作失败：" + e.message, "err");
    throw e;
  }
}
function showBatchResult(r, action) {
  toast(
    "已" +
      action +
      " " +
      (r.succeeded || 0) +
      " 项" +
      (r.failed ? "，失败 " + r.failed + " 项" : ""),
    r.failed ? "err" : "ok",
  );
  if (r.failed) {
    openResource("部分项目未完成", null, null);
    $("#resourceDetails").appendChild(
      mk(
        "p",
        null,
        "成功项目已取消选择，失败项目仍保留勾选。处理原因后可重试所选项目。",
      ),
    );
    $("#resourceDetails").appendChild(
      mk(
        "pre",
        "resource-result",
        (r.results || [])
          .filter(function (x) {
            return !x.ok;
          })
          .map(function (x) {
            return x.id + "：" + x.error;
          })
          .join("\n"),
      ),
    );
  }
}
function addAccount() {
  openResource("添加 DeepAI 账号", "添加账号", async function () {
    await api("/admin/api/accounts", {
      method: "POST",
      body: {
        action: "manual",
        api_key: $("#editCredential").value.trim(),
        name: $("#editName").value.trim(),
        group: $("#editGroup").value.trim() || "default",
      },
    });
    await refreshResources();
    toast("账号已添加，正在检查额度", "ok");
  });
  var methods = mk("div", "resource-toolbar");
  methods.append(
    actionButton("导入本机桌面端", async function () {
      await runAction(this, async function () {
        await api("/admin/api/accounts", {
          method: "POST",
          body: { action: "import" },
        });
        drawer.close();
        await refreshResources();
        toast("桌面端账号已导入", "ok");
      });
    }),
    actionButton("浏览器登录", function () {
      drawer.close();
      $("#oauthLogin").click();
    }),
  );
  $("#resourceDetails").append(
    methods,
    mk("p", "muted", "也可以直接填写已有的 DeepAI API Key。"),
  );
  inputField("editName", "账号名称（可选）", "", "text", false);
  inputField("editGroup", "所属分组", "default", "text", true);
  inputField("editCredential", "DeepAI API Key", "", "password", true);
  $("#editCredential").focus();
}

function keyEditor(k) {
  openResource(
    k ? "编辑 API 密钥" : "创建 API 密钥",
    k ? "保存修改" : "创建密钥",
    async function () {
      var body = {
        name: $("#editName").value.trim(),
        group: $("#editGroup").value.trim(),
      };
      if (k) body.id = k.id;
      var r = await api("/admin/api/keys", {
        method: k ? "PATCH" : "POST",
        body: body,
      });
      if (k) {
        await refreshResources();
        toast("密钥资料已保存", "ok");
        return;
      }
      // Keep newly created credentials visible until explicitly dismissed.
      if (!signedIn) return;
      openResource("密钥已创建", null, null);
      $("#resourceDetails").append(
        mk("p", null, "可在密钥详情中再次查看和复制。"),
        mk("code", "resource-secret", r.key),
      );
      $("#resourceDetails").appendChild(
        actionButton("复制密钥", async function () {
          try {
            await navigator.clipboard.writeText(r.key);
            toast("密钥已复制", "ok");
          } catch (e) {
            toast("请选中密钥并手动复制", "err");
          }
        }),
      );
      await refreshResources();
    },
  );
  if (k)
    detailPairs([
      ["密钥", k.key],
      ["状态", k.enabled ? "启用" : "停用"],
      ["请求数", k.requests],
      ["最近使用", fmtTime(k.last_used)],
    ]);
  if (k) secretControl("key", k.id, k.key);
  inputField("editName", "密钥备注", k ? k.name : "", "text", true);
  inputField(
    "editGroup",
    "授权分组",
    k ? k.group : "default",
    "text",
    true,
    "该密钥仅使用对应分组的账号；默认分组为 default。",
  );
  $("#editName").focus();
}
function keyMenu(k) {
  openResource("管理 API 密钥", null, null);
  detailPairs([
    ["备注", k.name],
    ["密钥", k.key],
    ["分组", k.group || "全部分组"],
    ["状态", k.enabled ? "启用" : "停用"],
  ]);
  secretControl("key", k.id, k.key);
  var bar = mk("div", "resource-toolbar");
  bar.append(
    actionButton("编辑资料", function () {
      keyEditor(k);
    }),
    actionButton(k.enabled ? "停用密钥" : "启用密钥", async function () {
      await runAction(this, async function () {
        await api("/admin/api/keys", {
          method: "PATCH",
          body: { id: k.id, enabled: !k.enabled },
        });
        drawer.close();
        await refreshResources();
        toast(k.enabled ? "密钥已停用" : "密钥已启用", "ok");
      });
    }),
    actionButton(
      "撤销密钥",
      async function () {
        if (
          !(await askConfirm(
            "撤销这个 API 密钥？",
            (k.name || k.key) +
              "\n使用此密钥的客户端将无法继续调用。此操作不可恢复。",
            "撤销密钥",
          ))
        )
          return;
        await runAction(this, async function () {
          await api("/admin/api/keys?id=" + encodeURIComponent(k.id), {
            method: "DELETE",
          });
          drawer.close();
          await refreshResources();
          toast("密钥已撤销", "ok");
        });
      },
      "danger",
    ),
  );
  $("#resourceDetails").appendChild(bar);
}
renderKeys = function (d) {
  var query = $("#keySearch").value.trim().toLowerCase(),
    status = $("#keyStatusFilter").value;
  var keys = (d.keys || []).filter(function (k) {
    return (
      [k.name, k.key, k.group].some(function (v) {
        return String(v || "")
          .toLowerCase()
          .includes(query);
      }) &&
      (!status || String(k.enabled) === status)
    );
  });
  keyPage = Math.max(
    0,
    Math.min(keyPage, Math.ceil(keys.length / keyPageSize) - 1),
  );
  var tb = $("#keys");
  tb.textContent = "";
  keys
    .slice(keyPage * keyPageSize, (keyPage + 1) * keyPageSize)
    .forEach(function (k) {
      var name = cell(""),
        statusCell = cell("", "state-column"),
        ops = cell("");
      name.append(
        actionButton(
          k.name || "未命名密钥",
          function () {
            keyEditor(k);
          },
          "resource-name",
        ),
        mk("div", "resource-sub numeric", k.key),
      );
      statusCell.appendChild(
        pill(k.enabled ? "启用" : "停用", k.enabled ? "ok" : ""),
      );
      ops.appendChild(
        actionButton("更多", function () {
          keyMenu(k);
        }),
      );
      tb.appendChild(
        row([
          name,
          cell(k.group || "全部", "secondary"),
          statusCell,
          cell(k.requests, "numeric secondary"),
          cell(fmtTime(k.last_used), "secondary"),
          ops,
        ]),
      );
    });
  if (!keys.length)
    tb.appendChild(emptyRow(6, "暂无匹配密钥。创建密钥后即可用于客户端调用。"));
  $("#keyPageInfo").textContent =
    "共 " + keys.length + " 项 · 第 " + (keyPage + 1) + " 页";
  $("#keyPrev").disabled = keyPage === 0;
  $("#keyNext").disabled = (keyPage + 1) * keyPageSize >= keys.length;
};

function resourceEndpoint(kind) {
  return kind === "google"
    ? "/admin/api/google/accounts"
    : "/admin/api/register/graph-mail";
}
async function reloadPool(kind) {
  if (kind === "google") await loadGooglePool(true);
  else await loadGraphMail();
}
function mailEditor(kind, account) {
  var isGoogle = kind === "google";
  openResource(
    account
      ? "更新邮箱凭据"
      : "添加" + (isGoogle ? " Google 账号" : " Graph 邮箱"),
    account ? "保存凭据" : "添加邮箱",
    async function () {
      var email = $("#editEmail").value.trim(),
        password = $("#editPassword").value;
      var body = { action: "update", email: email, password: password };
      if (!isGoogle) {
        body.client_id = $("#editClientID").value.trim();
        body.refresh_token = $("#editRefreshToken").value.trim();
      }
      if (!account) {
        var parts = isGoogle
          ? [email, password]
          : [email, password, body.client_id, body.refresh_token];
        if (
          parts.some(function (v) {
            return /\r|\n|----/.test(v);
          })
        )
          throw new Error("字段不能包含换行或分隔符 ----。");
        body = { content: parts.join("----") };
      }
      await api(resourceEndpoint(kind), { method: "POST", body: body });
      await reloadPool(kind);
      toast(account ? "邮箱凭据已更新，原有状态保留" : "邮箱已添加", "ok");
    },
  );
  if (account)
    detailPairs([
      [
        "当前状态",
        isGoogle
          ? googleNames[account.status] || account.status
          : { available: "可用", registered: "已注册", pending: "待确认" }[
              account.status
            ] || account.status,
      ],
      ["关联账号", account.account_id],
      ["最近结果", account.message],
    ]);
  inputField(
    "editEmail",
    "邮箱地址",
    account ? account.email : "",
    "email",
    true,
  ).readOnly = !!account;
  inputField(
    "editPassword",
    isGoogle ? "新的 Google 密码" : "邮箱密码",
    "",
    "password",
    true,
    "凭据不会回显；已有授权与注册记录会保留。",
  );
  if (!isGoogle) {
    inputField("editClientID", "Microsoft client_id", "", "text", true);
    inputField(
      "editRefreshToken",
      "Microsoft refresh_token",
      "",
      "password",
      true,
    );
  }
  if (account) $("#editPassword").focus();
  else $("#editEmail").focus();
}
function mailboxMenu(kind, a) {
  openResource("邮箱详情 · " + a.email, null, null);
  var linked = ((state.data && state.data.accounts) || []).find(function (x) {
    return (
      x.id === a.account_id ||
      (x.email || "").toLowerCase() === a.email.toLowerCase()
    );
  });
  $("#resourceDetails").appendChild(
    mk(
      "div",
      "resource-path",
      a.email +
        " → " +
        (kind === "google" ? "Google 授权" : "Graph 注册") +
        " → " +
        (linked ? "已关联 DeepAI 账号" : "尚未入池"),
    ),
  );
  detailPairs([
    [
      "状态",
      kind === "google"
        ? googleNames[a.status] || a.status
        : { available: "可用", registered: "已注册", pending: "待确认" }[
            a.status
          ] || a.status,
    ],
    ["当前步骤", a.phase],
    ["最近结果", a.message],
    ["更新时间", a.updated_at ? fmtTime(a.updated_at) : "—"],
  ]);
  var bar = mk("div", "resource-toolbar");
  bar.appendChild(
    actionButton("更新凭据", function () {
      mailEditor(kind, a);
    }),
  );
  if (linked)
    bar.appendChild(
      actionButton("查看入池账号", function () {
        accountDetails(linked);
      }),
    );
  if (kind === "google" && a.status !== "authorized")
    bar.appendChild(
      actionButton("授权此账号", async function () {
        await runAction(this, async function () {
          await startSelectedGoogle([a.email]);
          drawer.close();
        });
      }),
    );
  bar.appendChild(
    actionButton(
      "删除邮箱资料",
      async function () {
        await removeMail(kind, [a.email]);
      },
      "danger",
    ),
  );
  $("#resourceDetails").appendChild(bar);
}
async function removeMail(kind, emails) {
  if (
    !(await askConfirm(
      "删除 " + emails.length + " 个邮箱的登录资料？",
      emails.slice(0, 5).join("\n") +
        (emails.length > 5 ? "\n…" : "") +
        "\n仅移除本地登录资料；已入池的 DeepAI 账号与注册历史会保留。任务正在使用时无法删除。",
    ))
  )
    return;
  try {
    var r = await api(resourceEndpoint(kind), {
      method: "DELETE",
      body: { emails: emails },
    });
    var set = kind === "google" ? selectedGoogle : selectedMail;
    (r.results || []).forEach(function (x) {
      if (x.ok) set.delete(x.id);
    });
    if (drawer.open) drawer.close();
    await reloadPool(kind);
    showBatchResult(r, "删除");
  } catch (e) {
    toast(e.message, "err");
  }
}
renderMailAccounts = function () {
  var query = $("#mailSearch").value.trim().toLowerCase(),
    accounts = mailAccounts.filter(function (a) {
      return (
        a.email.toLowerCase().includes(query) &&
        (mailFilter === "all" || a.status === mailFilter)
      );
    });
  pruneSelection(
    selectedMail,
    mailAccounts.map(function (a) {
      return a.email;
    }),
  );
  mailPage = Math.max(
    0,
    Math.min(mailPage, Math.ceil(accounts.length / 20) - 1),
  );
  var tb = $("#graphAccounts");
  tb.textContent = "";
  accounts.slice(mailPage * 20, (mailPage + 1) * 20).forEach(function (a) {
    var name = cell(""),
      status = cell("", "state-column");
    name.appendChild(
      actionButton(
        a.email,
        function () {
          mailboxMenu("mail", a);
        },
        "resource-name",
      ),
    );
    status.appendChild(
      pill(
        { available: "可用", registered: "已注册", pending: "待确认" }[
          a.status
        ] || a.status,
        a.status === "registered" ? "ok" : a.status === "pending" ? "warn" : "",
      ),
    );
    var ops = cell("");
    ops.appendChild(
      actionButton("更多", function () {
        mailboxMenu("mail", a);
      }),
    );
    tb.appendChild(
      row([selectionCell(selectedMail, a.email, "Mail"), name, status, ops]),
    );
  });
  if (!accounts.length)
    tb.appendChild(
      emptyRow(
        4,
        mailAccounts.length
          ? "没有符合条件的邮箱"
          : "暂无邮箱，添加一条或导入邮箱文件。",
      ),
    );
  $("#mailPageInfo").textContent =
    "共 " + accounts.length + " 项 · 第 " + (mailPage + 1) + " 页";
  $("#mailPrev").disabled = mailPage === 0;
  $("#mailNext").disabled = (mailPage + 1) * 20 >= accounts.length;
  updateBulkBars();
};
renderGoogleAccounts = function () {
  var query = $("#googleSearch").value.trim().toLowerCase(),
    filter = $("#googleFilter").value;
  var accounts = googleAccounts.filter(function (a) {
    return (
      a.email.toLowerCase().includes(query) && (!filter || a.status === filter)
    );
  });
  pruneSelection(
    selectedGoogle,
    googleAccounts.map(function (a) {
      return a.email;
    }),
  );
  googlePage = Math.max(
    0,
    Math.min(googlePage, Math.ceil(accounts.length / 20) - 1),
  );
  var tb = $("#googleAccounts");
  tb.textContent = "";
  accounts.slice(googlePage * 20, (googlePage + 1) * 20).forEach(function (a) {
    var name = cell(""),
      status = cell("", "state-column"),
      ops = cell("");
    name.append(
      actionButton(
        a.email,
        function () {
          mailboxMenu("google", a);
        },
        "resource-name",
      ),
    );
    status.appendChild(
      pill(
        googleNames[a.status] || a.status,
        a.status === "authorized"
          ? "ok"
          : a.status === "failed"
            ? "err"
            : a.status === "action_required"
              ? "warn"
              : "",
      ),
    );
    ops.appendChild(
      actionButton("更多", function () {
        mailboxMenu("google", a);
      }),
    );
    tb.appendChild(
      row([
        selectionCell(selectedGoogle, a.email, "Google"),
        name,
        status,
        cell(a.message || "等待授权", "secondary google-message"),
        ops,
      ]),
    );
  });
  if (!accounts.length)
    tb.appendChild(
      emptyRow(5, "暂无匹配的 Google 账号。可以添加账号或调整筛选。"),
    );
  $("#googlePageInfo").textContent =
    "共 " + accounts.length + " 项 · 第 " + (googlePage + 1) + " 页";
  $("#googlePrev").disabled = googlePage === 0;
  $("#googleNext").disabled = (googlePage + 1) * 20 >= accounts.length;
  $("#googleSummary").textContent =
    "共 " +
    googleAccounts.length +
    " 个 · 已授权 " +
    googleAccounts.filter(function (a) {
      return a.status === "authorized";
    }).length +
    " · 失败 " +
    googleAccounts.filter(function (a) {
      return a.status === "failed";
    }).length;
  updateBulkBars();
};
async function startSelectedGoogle(emails) {
  await api("/admin/api/google/start", {
    method: "POST",
    body: { count: emails.length, emails: emails, retry_failed: true },
  });
  await loadGooglePool(true);
  goTab("tasks");
  toast("授权任务已启动，已授权账号会自动跳过", "ok");
}
async function previewImport(kind, button) {
  var isGoogle = kind === "google",
    fileInput = $(isGoogle ? "#googleFile" : "#graphMailFile"),
    file = fileInput.files[0],
    status = $(isGoogle ? "#googleImportStatus" : "#graphMailStatus");
  if (!file || file.size > 3 * 1024 * 1024) {
    status.textContent = "请选择不超过 3 MB 的 UTF-8 文本文件";
    return;
  }
  await runAction(button, async function () {
    status.textContent = "正在检查文件…";
    var content = await file.text();
    try {
      var p = await api(resourceEndpoint(kind), {
        method: "POST",
        body: { content: content, preview: true },
      });
      status.textContent =
        "预览：新增 " + p.added + " 个，更新 " + p.updated + " 个";
      if (
        !(await askConfirm(
          "确认导入邮箱资料？",
          "新增 " +
            p.added +
            " 个 · 更新 " +
            p.updated +
            " 个\n重复邮箱会更新凭据，保留已有状态。预览不展示密码或令牌。",
          "确认导入",
        ))
      ) {
        status.textContent = "已取消导入，未修改资料";
        return;
      }
      var r = await api(resourceEndpoint(kind), {
        method: "POST",
        body: { content: content },
      });
      fileInput.value = "";
      status.textContent =
        "导入完成：新增 " + r.added + " 个，更新 " + r.updated + " 个";
      await reloadPool(kind);
      toast(status.textContent, "ok");
    } catch (e) {
      status.textContent = "导入失败：" + e.message;
      throw e;
    } finally {
      content = "";
    }
  });
}

// Navigation separates maintained resources from their execution history.
function setupResourceNavigation() {
  var tasks = mk("section");
  tasks.id = "tab-tasks";
  $("main").appendChild(tasks);
  resourceHeading(
    tasks,
    "任务中心",
    "创建任务、处理异常，并查看注册与授权进度。",
  );
  var layout = mk("div", "task-layout");
  tasks.appendChild(layout);
  var googleTask = $("#googleStart").closest(".task-card");
  googleTask.classList.add("card", "google-pool");
  layout.appendChild(googleTask);
  layout.appendChild($("#registerForm"));
  layout.appendChild($("#regStatus").closest(".recent-card"));
  layout.appendChild($("#taskDetails"));
  $("#googlePool .register-layout").style.display = "block";
  $("#tab-register > .register-layout").style.display = "block";
  $("#tab-register .register-side").remove();
  var nav = mk("button", null, "任务中心");
  nav.dataset.tab = "tasks";
  $('nav button[data-tab="register"]').after(nav);
  $('nav button[data-tab="register"]').textContent = "邮箱资产";
  $('nav button[data-tab="accounts"]').textContent = "账号池";
  $("#tab-register .page-heading h1").textContent = "邮箱资产";
  $("#tab-register .page-heading p").textContent =
    "维护邮箱登录资料，查看授权结果与入池关联。";
  $("#openRegister").textContent = "前往任务中心";
  $("#openRegister").onclick = function () {
    goTab("tasks");
  };
  var tabs = mk("div", "resource-tabs"),
    graphBlock = $("#tab-register > .register-layout"),
    summary = $("#graphSummary");
  function provider(kind) {
    var google = kind === "google";
    $("#googlePool").hidden = !google;
    graphBlock.hidden = google;
    summary.hidden = google;
    Array.from(tabs.children).forEach(function (b) {
      b.setAttribute("aria-pressed", b.dataset.provider === kind);
    });
  }
  [
    ["mail", "Graph 邮箱"],
    ["google", "Google 账号"],
  ].forEach(function (x) {
    var b = actionButton(x[1], function () {
      provider(x[0]);
    });
    b.dataset.provider = x[0];
    tabs.appendChild(b);
  });
  $("#tab-register .page-heading").after(tabs);
  provider("mail");
  showImport = function () {
    goTab("register");
    provider("mail");
    $("#importPanel").open = true;
    $("#importPanel").scrollIntoView({ block: "center" });
  };
  showRegister = function () {
    goTab("tasks");
    $("#registerForm").scrollIntoView({ block: "center" });
  };
  $("#quickImport").onclick = showImport;
  $("#openImport").onclick = showImport;
  $("#quickRegister").onclick = showRegister;
  $("#overviewRegister").onclick = showRegister;
  $("#showTaskDetails").onclick = function () {
    $("#taskDetails").scrollIntoView({ block: "start" });
  };
  function activate(tab, writeHash) {
    var button = $('nav button[data-tab="' + tab + '"]');
    if (!button) return;
    document.querySelectorAll("nav button").forEach(function (b) {
      b.classList.toggle("on", b === button);
      if (b === button) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    });
    document.querySelectorAll("main > section").forEach(function (s) {
      s.classList.toggle("on", s.id === "tab-" + tab);
    });
    $("#pageName").textContent = button.textContent;
    if (writeHash && location.hash !== "#" + tab)
      history.pushState(null, "", "#" + tab);
    if (signedIn) {
      if (tab === "logs") refreshLogs();
      if (tab === "register" || tab === "tasks") {
        loadRegisterJobs();
        loadGooglePool(true);
      }
    }
  }
  document.querySelectorAll("nav button").forEach(function (b) {
    b.onclick = function () {
      activate(b.dataset.tab, true);
    };
  });
  window.addEventListener("popstate", function () {
    activate(location.hash.slice(1) || "overview", false);
  });
  activate(location.hash.slice(1) || "overview", false);
}

function setupResources() {
  var accountSection = $("#tab-accounts"),
    accountCard = accountSection.querySelector(".card");
  var add = actionButton("添加账号", addAccount, "primary");
  add.id = "addAccount";
  resourceHeading(
    accountSection,
    "账号池",
    "管理可调用的 DeepAI 账号，按状态与余额安排使用。",
    add,
  );
  accountCard.querySelector("h2").hidden = true;
  $("#manualName").parentElement.hidden = true;
  $("#importLocal").hidden = true;
  $("#oauthLogin").hidden = true;
  $("#checkinAll").textContent = "刷新全部额度";
  $("#acctFilterReset").textContent = "清除";
  $("#acctFilterReset").title = "清除全部筛选";
  $("#checkinAll").onclick = function () {
    return runAction(this, function () {
      return checkin(null);
    });
  };
  var counts = mk("div", "resource-counts");
  counts.id = "acctCounts";
  var controls = mk("div", "account-controls");
  $("#checkinAll").parentElement.hidden = true;
  controls.append(counts, $("#checkinAll"), $(".account-filters"));
  accountCard.prepend(controls);
  var sort = mk("select");
  sort.id = "acctSort";
  sort.setAttribute("aria-label", "账号排序");
  [
    ["", "默认顺序"],
    ["balance", "余额从高到低"],
    ["recent", "最近使用"],
    ["name", "账号名称"],
  ].forEach(function (v) {
    sort.appendChild(new Option(v[1], v[0]));
  });
  $(".account-filters").appendChild(sort);
  sort.onchange = function () {
    renderAccounts(state.data || {});
  };
  $("#acctSelectAll").onchange = function () {
    var checked = this.checked;
    visibleAccountIDs.forEach(function (id) {
      if (checked) selectedAccounts.add(id);
      else selectedAccounts.delete(id);
    });
    renderAccounts(state.data || {});
  };
  $("#acctBulkEnable").onclick = function () {
    var b = this;
    runAction(b, function () {
      return batchAccounts("enable", Array.from(selectedAccounts));
    });
  };
  $("#acctBulkDisable").onclick = function () {
    var b = this;
    runAction(b, function () {
      return batchAccounts("disable", Array.from(selectedAccounts));
    });
  };
  $("#acctBulkRemove").onclick = async function () {
    var ids = Array.from(selectedAccounts);
    if (
      await askConfirm(
        "删除所选 " + ids.length + " 个账号？",
        "将从网关账号池移除所选账号，不会删除上游账号或邮箱资料。",
      )
    )
      await runAction(this, function () {
        return batchAccounts("remove", ids);
      });
  };
  $("#acctBulkGroup").onclick = function () {
    var ids = Array.from(selectedAccounts);
    openResource(
      "修改 " + ids.length + " 个账号的分组",
      "保存分组",
      async function () {
        await batchAccounts("set-group", ids, $("#editGroup").value.trim());
      },
    );
    inputField("editGroup", "目标分组", "default", "text", true);
    $("#editGroup").focus();
  };
  $("#acctBulkQuota").onclick = async function () {
    var ids = Array.from(selectedAccounts),
      button = this;
    await runAction(this, async function () {
      var results = [],
        index = 0;
      async function worker() {
        while (index < ids.length && signedIn) {
          var id = ids[index++];
          try {
            var r = await api("/admin/api/checkin", {
              method: "POST",
              body: { id: id },
            });
            var failed = (r.results || []).find(function (x) {
              return !x.ok;
            });
            results.push({ id: id, ok: !failed, error: failed && failed.err });
          } catch (e) {
            results.push({ id: id, ok: false, error: e.message });
          }
          button.textContent = "刷新额度 " + results.length + "/" + ids.length;
        }
      }
      try {
        await Promise.all(
          Array.from({ length: Math.min(4, ids.length) }, worker),
        );
      } finally {
        button.textContent = "刷新额度";
      }
      if (!signedIn) return;
      results.forEach(function (r) {
        if (r.ok) selectedAccounts.delete(r.id);
      });
      await refreshResources();
      showBatchResult(
        {
          results: results,
          succeeded: results.filter(function (x) {
            return x.ok;
          }).length,
          failed: results.filter(function (x) {
            return !x.ok;
          }).length,
        },
        "刷新额度",
      );
    });
  };
  $("#acctClearSelection").onclick = function () {
    selectedAccounts.clear();
    renderAccounts(state.data || {});
  };
  resourceHeading(
    $("#tab-keys"),
    "API 密钥",
    "为客户端签发密钥，并按分组控制可用账号。",
    actionButton(
      "创建密钥",
      function () {
        keyEditor(null);
      },
      "primary",
    ),
  );
  $("#keyName").parentElement.hidden = true;
  $("#tab-keys h2").hidden = true;
  $("#keySearch").oninput = $("#keyStatusFilter").onchange = function () {
    keyPage = 0;
    renderKeys(state.data || {});
  };
  $("#keyPrev").onclick = function () {
    keyPage--;
    renderKeys(state.data || {});
  };
  $("#keyNext").onclick = function () {
    keyPage++;
    renderKeys(state.data || {});
  };
  $("#googleAdd").onclick = function () {
    mailEditor("google", null);
  };
  $("#mailAdd").onclick = function () {
    mailEditor("mail", null);
  };
  $("#googleFilter").onchange = function () {
    googlePage = 0;
    renderGoogleAccounts();
  };
  $("#googleBulkRemove").onclick = function () {
    return runAction(this, function () {
      return removeMail("google", Array.from(selectedGoogle));
    });
  };
  $("#mailBulkRemove").onclick = function () {
    return runAction(this, function () {
      return removeMail("mail", Array.from(selectedMail));
    });
  };
  $("#googleBulkRetry").onclick = function () {
    return runAction(this, function () {
      return startSelectedGoogle(Array.from(selectedGoogle));
    });
  };
  $("#googleClearSelection").onclick = function () {
    selectedGoogle.clear();
    renderGoogleAccounts();
  };
  $("#mailClearSelection").onclick = function () {
    selectedMail.clear();
    renderMailAccounts();
  };
  $("#googleSelectPage").onclick = function () {
    Array.from(
      $("#googleAccounts").querySelectorAll("input[type=checkbox]"),
    ).forEach(function (b) {
      selectedGoogle.add(b.dataset.selectGoogle);
    });
    renderGoogleAccounts();
  };
  $("#mailSelectPage").onclick = function () {
    Array.from(
      $("#graphAccounts").querySelectorAll("input[type=checkbox]"),
    ).forEach(function (b) {
      selectedMail.add(b.dataset.selectMail);
    });
    renderMailAccounts();
  };
  $("#googleImport").onclick = function () {
    return previewImport("google", this);
  };
  $("#graphMailImport").onclick = function () {
    return previewImport("mail", this);
  };
  setupResourceNavigation();
  $("#tab-settings").addEventListener("input", function (event) {
    if (event.target.closest("#egressPanel")) return;
    settingsDirty = true;
    $("#settingsDraft").hidden = false;
  });
  var oldSettings = renderSettings;
  renderSettings = function (s) {
    if (!settingsDirty) oldSettings(s);
  };
  $("#saveSettings").onclick = async function () {
    var button = this;
    await runAction(button, async function () {
      var aliases;
      try {
        aliases = JSON.parse($("#sAliases").value || "{}");
      } catch (e) {
        throw new Error("模型别名必须是合法 JSON 对象");
      }
      if (
        !aliases ||
        Array.isArray(aliases) ||
        typeof aliases !== "object" ||
        Object.values(aliases).some(function (x) {
          return typeof x !== "string";
        })
      )
        throw new Error("模型别名必须是名称到模型 ID 的对应关系");
      var body = {
        model_aliases: aliases,
        cors_origin: $("#sCors").value.trim(),
        fallback_model: $("#sFallback").value.trim(),
      };
      [
        ["sTimeout", "request_timeout_sec"],
        ["sRetries", "max_retries"],
        ["sLogKeep", "log_keep"],
      ].forEach(function (pair) {
        var n = Number($("#" + pair[0]).value);
        if (!Number.isInteger(n) || n < 1)
          throw new Error("超时、重试次数和日志条数必须为正整数");
        body[pair[1]] = n;
      });
      var password = $("#sPw").value;
      if (password) body.admin_password = password;
      var saveSession = sessionVersion;
      var fields = Array.from(
        $("#tab-settings").querySelectorAll("input,textarea,select"),
      );
      fields.forEach(function (f) {
        f.disabled = true;
      });
      var r;
      try {
        r = await api("/admin/api/settings", { method: "POST", body: body });
      } finally {
        if (saveSession === sessionVersion)
          fields.forEach(function (f) {
            f.disabled = false;
          });
      }
      if (!signedIn) return;
      if (password) adminPassword = password;
      $("#sPw").value = "";
      settingsDirty = false;
      $("#settingsDraft").hidden = true;
      renderSettings(r.settings || {});
      toast("设置已保存", "ok");
    });
  };
  window.addEventListener("beforeunload", function (e) {
    if (settingsDirty || drawerDirty) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
  $("#toast").setAttribute("role", "status");
  $("#toast").setAttribute("aria-live", "polite");
  var originalLogin = showLogin;
  showLogin = function (message) {
    accountRefreshVersion++;
    drawerGeneration++;
    regPollVersion++;
    drawerBusy = false;
    settingsDirty = false;
    drawerDirty = false;
    $("#settingsDraft").hidden = true;
    if (drawer.open) drawer.close();
    if (confirmBox.open) finishConfirm(false);
    selectedAccounts.clear();
    selectedGoogle.clear();
    selectedMail.clear();
    mailAccounts = [];
    googleAccounts = [];
    latestLogs = [];
    if (googleTimer) clearTimeout(googleTimer);
    if (regTimer) clearTimeout(regTimer);
    stopOAuth();
    $("#sPw").value = "";
    $("#tab-settings")
      .querySelectorAll("input,textarea,select")
      .forEach(function (f) {
        f.disabled = false;
      });
    originalLogin(message);
  };
  updateBulkBars();
}
function setupLogsAndTasks() {
  var logCard = $("#tab-logs .card"),
    filters = mk("div", "resource-toolbar");
  var search = mk("input");
  search.id = "logSearch";
  search.type = "search";
  search.placeholder = "搜索模型、账号邮箱或错误";
  search.setAttribute("aria-label", search.placeholder);
  var status = mk("select");
  status.id = "logStatusFilter";
  status.setAttribute("aria-label", "请求结果");
  [
    ["", "全部结果"],
    ["ok", "成功"],
    ["error", "失败"],
  ].forEach(function (x) {
    status.appendChild(new Option(x[1], x[0]));
  });
  var period = mk("select");
  period.id = "logTimeFilter";
  period.setAttribute("aria-label", "日志时间范围");
  [
    ["0", "全部时间"],
    ["3600", "最近 1 小时"],
    ["86400", "最近 24 小时"],
    ["604800", "最近 7 天"],
  ].forEach(function (x) {
    period.appendChild(new Option(x[1], x[0]));
  });
  filters.append(search, status, period);
  logCard.querySelector("h2").after(filters);
  var note = mk("p", "hint");
  note.textContent =
    "筛选当前已加载的日志；需要更多历史记录时，将读取条数切换为「全部」。";
  filters.after(note);
  var originalLogs = renderLogs;
  function filterLogs() {
    var query = search.value.trim().toLowerCase(),
      cutoff = Number(period.value)
        ? Date.now() / 1000 - Number(period.value)
        : 0;
    var accounts = new Map(
      ((state.data && state.data.accounts) || []).map(function (a) {
        return [a.id, a.email || a.name];
      }),
    );
    originalLogs(
      latestLogs.filter(function (l) {
        return (
          (!cutoff || l.time >= cutoff) &&
          (!status.value ||
            (status.value === "error" ? l.status >= 400 : l.status < 400)) &&
          [l.model, l.account, accounts.get(l.account), l.err, l.endpoint].some(
            function (v) {
              return String(v || "")
                .toLowerCase()
                .includes(query);
            },
          )
        );
      }),
    );
  }
  renderLogs = function (logs) {
    latestLogs = logs || [];
    filterLogs();
  };
  search.oninput = status.onchange = period.onchange = filterLogs;
  $("#clearLogs").onclick = async function () {
    if (
      await askConfirm(
        "清空全部请求日志？",
        "将清空全部保留的请求日志，不限于当前筛选结果。",
        "清空日志",
      )
    )
      await runAction(this, async function () {
        await api("/admin/api/logs", { method: "DELETE" });
        await refreshLogs();
        toast("请求日志已清空", "ok");
      });
  };
  $("#clearStats").onclick = async function () {
    if (
      await askConfirm(
        "重置累计统计？",
        "累计请求、Token 和成本统计将归零，账号和密钥保持不变。",
        "重置统计",
      )
    )
      await runAction(this, async function () {
        await api("/admin/api/stats", { method: "DELETE" });
        await refreshResources();
        toast("累计统计已重置", "ok");
      });
  };
  var list = mk("select");
  list.id = "registerHistory";
  list.setAttribute("aria-label", "选择注册任务");
  $("#regStatus").before(list);
  var info = mk(
    "p",
    "hint",
    "注册任务保留当前进程内最近 24 小时记录；Google 授权显示最近一次任务。",
  );
  list.before(info);
  loadRegisterJobs = async function () {
    var generation = ++regPollVersion;
    if (regTimer) {
      clearTimeout(regTimer);
      regTimer = null;
    }
    try {
      var r = await api("/admin/api/register"),
        jobs = r.jobs || [],
        selected = list.value;
      if (!signedIn || generation !== regPollVersion) return;
      jobs.sort(function (a, b) {
        return (b.started_at || 0) - (a.started_at || 0);
      });
      list.textContent = "";
      jobs.forEach(function (j) {
        list.appendChild(
          new Option(fmtTime(j.started_at) + " · " + regStatusText(j), j.id),
        );
      });
      if (!jobs.length) {
        list.appendChild(new Option("暂无注册任务", ""));
        return;
      }
      if (
        jobs.some(function (j) {
          return j.id === selected;
        })
      )
        list.value = selected;
      var job =
        jobs.find(function (j) {
          return j.id === list.value;
        }) || jobs[0];
      renderRegister(job);
      $("#regStatus").textContent = regStatusText(job);
      regId = job.id;
      if (job.running) pollRegister(job.id);
    } catch (e) {
      if (signedIn && generation === regPollVersion)
        $("#regStatus").textContent = "任务加载失败：" + e.message;
    }
  };
  list.onchange = function () {
    if (list.value) pollRegister(list.value);
  };
  $("#startRegister").onclick = async function () {
    var count = Number($("#regCount").value),
      concurrency = Number($("#regConcurrency").value);
    if (
      !Number.isInteger(count) ||
      count < 1 ||
      count > 1000 ||
      !Number.isInteger(concurrency) ||
      concurrency < 1 ||
      concurrency > 10
    ) {
      toast("注册数量须为 1–1000，并发数须为 1–10", "err");
      return;
    }
    await runAction(this, async function () {
      var r = await api("/admin/api/register", {
        method: "POST",
        body: {
          count: count,
          concurrency: concurrency,
          mail_backend: "graph",
          proxy_pool: $("#regProxyPool").value.trim(),
        },
      });
      list.value = "";
      await loadRegisterJobs();
      list.value = r.id;
      pollRegister(r.id);
      toast("注册任务已启动", "ok");
    });
  };
  var oldOverview = renderOverview;
  renderOverview = function (d) {
    oldOverview(d);
    var accounts = d.accounts || [],
      known = accounts.filter(function (a) {
        return Number.isFinite(accountBalance(a));
      });
    var total = known.reduce(function (n, a) {
      return n + accountBalance(a);
    }, 0);
    var cards = $("#kpis");
    cards.textContent = "";
    [
      [
        "可用账号",
        accounts.filter(function (a) {
          return a.enabled && a.status === "ok";
        }).length,
        "按最近一次检测结果",
        function () {
          $("#acctStatusFilter").value = "ok";
          goTab("accounts");
          renderAccounts(state.data);
        },
      ],
      [
        "已知余额合计",
        known.length ? fmtNum(total) : "—",
        known.length + "/" + accounts.length + " 个账号已有额度记录",
        function () {
          $("#acctBalanceFilter").value = "positive";
          goTab("accounts");
          renderAccounts(state.data);
        },
      ],
      [
        "异常账号",
        accounts.filter(function (a) {
          return a.enabled && a.status === "error";
        }).length,
        "查看错误原因与处理入口",
        function () {
          $("#acctStatusFilter").value = "error";
          goTab("accounts");
          renderAccounts(state.data);
        },
      ],
      [
        "累计请求",
        fmtNum((d.stats || {}).total_requests),
        "失败 " + fmtNum((d.stats || {}).failed_requests) + " 次",
        function () {
          goTab("logs");
        },
      ],
    ].forEach(function (x) {
      var b = mk("button", "kpi kpi-action");
      b.type = "button";
      b.append(
        mk("div", "k", x[0]),
        mk("div", "v numeric", x[1]),
        mk("div", "resource-sub", x[2]),
      );
      b.onclick = x[3];
      cards.appendChild(b);
    });
  };
}
setupResources();
setupLogsAndTasks();
