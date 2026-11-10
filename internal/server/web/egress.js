(function setupEgress() {
  var panel = $("#egressPanel"), dirty = false, busy = false, loading = false, loaded = false, current = null;
  var controls = ["egressDetect", "egressRotate", "egressPause", "egressResume"];
  function buttons() {
    controls.forEach(function(id) { $("#" + id).disabled = busy || dirty || !current || !current.config.enabled || !!current.status.busy; });
    $("#egressPause").disabled = busy || dirty || !current || !current.config.enabled;
    $("#egressSave").disabled = busy || !!(current && current.status.busy);
    $("#egressDraft").hidden = !dirty;
  }
  function render(data, fields) {
    current = data;
    var c = data.config, s = data.status;
    $("#egressBadge").textContent = !c.enabled ? "未启用" : s.paused ? "已暂停" : s.busy ? "处理中" : "就绪";
    $("#egressIP").textContent = s.ip || "尚未检测";
    $("#egressCount").textContent = s.count + (s.pending ? " + " + s.pending + " 待核对" : "") + " / " + c.threshold;
    $("#egressRotated").textContent = s.last_rotation ? new Date(s.last_rotation * 1000).toLocaleString() : "尚未切换";
    $("#egressMessage").textContent = s.last_error || "";
    if (fields && !dirty) {
      $("#egressEnabled").checked = c.enabled;
      $("#egressAuto").checked = c.auto_rotate;
      $("#egressLimit").value = c.threshold;
      $("#egressWait").value = c.verify_timeout_sec;
      $("#egressIPURL").value = c.ip_url;
      $("#egressProxy").placeholder = c.proxy_set ? "已保存，留空保持原值" : "http://用户名:密码@主机:端口";
      $("#egressRefresh").placeholder = c.refresh_set ? "已保存，留空保持原值" : "https://供应商/刷新链接";
      $("#egressProxyHint").textContent = c.proxy_set ? "当前代理：" + c.proxy_label + "（认证信息隐藏）" : "支持 HTTP、HTTPS、SOCKS5。保存后不回显凭据。";
      $("#egressRefreshHint").textContent = c.refresh_set ? "刷新链接已保存；保存配置不会调用它。" : "每次切换只请求一次，随后确认新出口。";
    }
    buttons();
  }
  async function load() {
    if (!signedIn || loading) return;
    loading = true;
    try { var d = await api("/admin/api/egress"); if (signedIn) { render(d, !loaded); loaded = true; } }
    catch(e) { if (signedIn) $("#egressMessage").textContent = "读取代理状态失败：" + e.message; }
    finally { loading = false; }
  }
  panel.addEventListener("input", function() { dirty = true; buttons(); });
  async function action(button, fn) {
    if (busy) return;
    busy = true; var version = sessionVersion;
    panel.setAttribute("aria-busy", "true");
    panel.querySelectorAll("input").forEach(function(f){f.disabled = true;}); buttons();
    try { await fn(); }
    catch (e) { if (version === sessionVersion && signedIn) { $("#egressMessage").textContent = e.message; toast(e.message, "err"); } }
    finally { busy = false; panel.removeAttribute("aria-busy"); if (version === sessionVersion) { panel.querySelectorAll("input").forEach(function(f){f.disabled = false;}); await load(); buttons(); } }
  }
  $("#egressSave").onclick = function() {
    return action(this, async function() {
      var limit = Number($("#egressLimit").value), wait = Number($("#egressWait").value);
      if (!Number.isInteger(limit) || limit < 1 || limit > 1000) throw new Error("切换阈值须为 1–1000 的整数");
      if (!Number.isInteger(wait) || wait < 1 || wait > 120) throw new Error("确认超时须为 1–120 秒的整数");
      var data = await api("/admin/api/egress", {method:"POST", body:{enabled:$("#egressEnabled").checked, auto_rotate:$("#egressAuto").checked, threshold:limit, verify_timeout_sec:wait, proxy_url:$("#egressProxy").value.trim(), refresh_url:$("#egressRefresh").value.trim(), ip_url:$("#egressIPURL").value.trim()}});
      dirty = false; $("#egressProxy").value = ""; $("#egressRefresh").value = "";
      render(data, true); toast("代理配置已保存", "ok");
    });
  };
  [["egressDetect","detect"],["egressRotate","rotate"],["egressPause","pause"],["egressResume","resume"]].forEach(function(pair){
    $("#" + pair[0]).onclick = function() {
      if (dirty) return;
      return action(this, async function(){
        $("#egressMessage").textContent = pair[1] === "rotate" ? "等待在途请求结束，刷新出口并确认连接…" : "正在处理，请稍候…";
        var data = await api("/admin/api/egress/action", {method:"POST",body:{action:pair[1]}});
        render(data, false); toast(pair[1] === "rotate" ? "新出口已确认" : "代理状态已更新", "ok");
      });
    };
  });
  var oldRender = renderSettings;
  renderSettings = function(s){oldRender(s); if (!loaded) load();};
  var oldLogin = showLogin;
  showLogin = function(message){dirty = false; loaded = false; current = null; $("#egressProxy").value = ""; $("#egressRefresh").value = ""; $("#egressIP").textContent = "—"; $("#egressMessage").textContent = ""; oldLogin(message); buttons();};
  window.addEventListener("beforeunload", function(e){if(dirty){e.preventDefault();e.returnValue="";}});
  setInterval(function(){if(signedIn && !busy && $("#tab-settings").classList.contains("on")) load();}, 3000);
  $('nav button[data-tab="settings"]').addEventListener("click", load);
  buttons();
})();
