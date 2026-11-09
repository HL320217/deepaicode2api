function (args) {
  if (location.origin !== args.origin) return {};
  const visible = el => el && el.getClientRects().length && !el.disabled;
  const find = selector => Array.from(document.querySelectorAll(selector)).find(visible);
  const result = (phase, message, submitted) => ({phase, message, submitted: submitted || ''});
  if (args.mode === 'deepai') {
    const button = find('button.social-auth-btn#social-auth-google, button.signin-with-google, #social-auth-google');
    if (button && !args.attempted.deepai) {
      button.click();
      return result('google', '正在打开 Google 授权页', 'deepai');
    }
    return {};
  }
  const text = document.body ? document.body.innerText : '';
  if (/^Error 5\d\d\b/i.test(document.title) && /^\s*5\d\d\./.test(text))
    return {phase: 'google_server_error', terminalError: 'google_server_error'};
  if (/browser or app may not be secure|此浏览器或应用可能不安全|這個瀏覽器或應用程式可能不安全/i.test(text))
    return {phase: 'browser_rejected', terminalError: 'browser_rejected'};
  if (/^\/(?:v\d+\/)?signin\/rejected\/?$/.test(location.pathname))
    return {phase: 'signin_rejected', terminalError: 'signin_rejected'};
  const manual = (message, required) => ({...result('action_required', message), interactionRequired: !!required});
  const loadingSetup = message => {
    // State lives only on this temporary page, not in browser storage. Allow
    // async controls to render, but don't wait forever if setup is mandatory.
    let wait = document.__deepaiCodeSetupWait;
    if (!wait || wait.path !== location.pathname) {
      wait = document.__deepaiCodeSetupWait = {path: location.pathname, started: performance.now()};
    }
    if (performance.now() - wait.started >= 5000)
      return manual('Google 设置页未能继续或跳过，请在有界面环境处理', true);
    return result('onboarding', message);
  };
  if (/wrong password|incorrect password|couldn.t find your google account|couldn.t sign you in|密码错误|密码不正确|找不到.*Google|无法.*登录/i.test(text))
    return manual('Google 拒绝了自动登录，请在浏览器检查密码或登录限制；也可以跳过此账号', true);
  if (find('iframe[src*="recaptcha"], input[name="totpPin"], input[name="idvPin"], input[name="Pin"], input[name="ootpPin"], input[name="knowledgePreregisteredEmailResponse"]'))
    return manual('请在浏览器完成 Google 验证码、双重验证或身份确认，完成后会自动继续', true);
  const buttons = Array.from(document.querySelectorAll('button, [role="button"], a')).filter(visible);
  // Workspace first-use information is shown before OAuth consent. Restrict
  // acknowledgements to this exact Google page; never accept arbitrary terms.
  if (/^\/(?:v\d+\/)?signin\/speedbump\/workspacetermsofservice\/?$/.test(location.pathname)) {
    const acknowledge = buttons.find(el => /^(I understand|我了解|我瞭解)$/i.test(el.innerText.trim()));
    const key = 'workspace-notice:' + location.pathname;
    if (acknowledge && !args.attempted[key]) {
      acknowledge.click();
      return result('onboarding', '已确认 Workspace 首次登录告知页', key);
    }
    if (args.attempted[key]) return loadingSetup('等待 Workspace 告知页跳转');
    return loadingSetup('正在加载 Workspace 首次登录告知页');
  }
  // These known optional setup pages may themselves mention verification or
  // contain a phone input. Only dismiss them via an explicit "Not now" action.
  if (/^\/(?:v\d+\/)?signin\/speedbump\/(?:passkeyenrollment|phonecollection)\/?$/.test(location.pathname)) {
    const later = buttons.find(el => /^(Not now|Maybe later|Do this later|以后再说|稍后再说|稍後再說)$/i.test(el.innerText.trim()));
    const key = 'setup-later:' + location.pathname;
    if (later && !args.attempted[key]) {
      later.click();
      return result('onboarding', '已选择“以后再说”，继续 Google 授权', key);
    }
    if (args.attempted[key]) return loadingSetup('等待 Google 可选设置页跳转');
    return loadingSetup('正在加载 Google 可选设置页');
  }
  if (find('input[type="tel"]') || /2-Step Verification|两步验证|双重验证|Check your phone|查看您的手机|Verify it.s you|验证您的身份/i.test(text))
    return manual('请在浏览器完成 Google 验证码、双重验证或身份确认，完成后会自动继续', true);
  if (/^\/(?:v\d+\/)?signin\/speedbump\//.test(location.pathname))
    return manual('Google 登录设置页需要确认，请在浏览器处理后继续；程序不会修改密码或安全设置', true);
  const fill = (input, value) => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(input, value);
    input.dispatchEvent(new Event('input', {bubbles: true}));
    input.dispatchEvent(new Event('change', {bubbles: true}));
  };
  const email = find('input#identifierId, input[type="email"][name="identifier"]');
  if (email) {
    if (!args.attempted.email) {
      const next = find('#identifierNext');
      if (next) { fill(email, args.email); next.click(); return result('email', '已提交 Google 邮箱', 'email'); }
    }
    return result('email', '等待 Google 检查账号');
  }
  const password = find('input[type="password"][name="Passwd"]');
  if (password) {
    if (!args.attempted.password) {
      const next = find('#passwordNext');
      if (next) { fill(password, args.password); next.click(); return result('password', '已提交 Google 密码', 'password'); }
    }
    return result('password', '等待 Google 验证密码');
  }
  const account = Array.from(document.querySelectorAll('[data-identifier]')).find(el => visible(el) && el.dataset.identifier.toLowerCase() === args.email.toLowerCase());
  if (account && !args.attempted.choose) { account.click(); return result('google', '已选择导入的 Google 账号', 'choose'); }
  const another = buttons.find(el => /^(Use another account|使用其他账号|使用另一个帐号)$/i.test(el.innerText.trim()));
  if (another && !args.attempted.another) { another.click(); return result('google', '正在切换到导入的 Google 账号', 'another'); }
  if (/^\/(?:v\d+\/)?signin\/oauth(?:\/|$)|^\/o\/oauth2\/(?:v2\/)?auth\/?$/.test(location.pathname) && /deepai/i.test(text)) {
    const approve = find('#submit_approve_access') || buttons.find(el => /^(Continue|Allow|继续|允许)$/i.test(el.innerText.trim()));
    const consentKey = 'consent:' + location.pathname;
    if (approve && !args.attempted[consentKey]) { approve.click(); return result('consent', '正在确认 DeepAI 的 Google 授权', consentKey); }
  }
  return manual('请查看 Google 页面并完成额外确认，完成后会自动继续');
}
