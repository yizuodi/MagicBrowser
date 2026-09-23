// Magic Browser 外壳逻辑：密码门、地址栏导航、iframe 生命周期、
// 加载遮罩状态机（IDLE → GENERATING → IDLE）与 bridge postMessage 闭环。
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };

  var state = {
    sessionId: null,      // 服务端会话 ID（首次 navigate 获得）
    loadingText: '页面加载中…',
    generating: false,    // UI 锁状态机
    currentUrl: '',       // 当前展示页的 URL（展示用）
    history: [],          // {url, pageIndex}
    historyPos: -1,
    currentFrame: null,
    lastNavUrl: null,     // 最近一次「真实导航」（重新生成用）
    lastAction: null,
    currentHistoryId: ''  // 当前页的持久化历史 id（分享用；生成完成即有效）
  };

  // ?resume=<历史id>：从分享/后台「继续」进来，以该页为上下文种子。
  var resumeId = new URLSearchParams(location.search).get('resume');

  // ---------- 启动 ----------
  fetchPublicConfig(function (pub) {
    state.loadingText = pub.loading_text || state.loadingText;
    $('loading-text').textContent = state.loadingText;
    if (pub.gate_enabled) {
      probeAuth(function (ok) { ok ? showChrome() : showGate(); });
    } else {
      showChrome();
    }
  });

  function fetchPublicConfig(cb) {
    fetch('/api/v1/config/public').then(function (r) { return r.json(); })
      .then(function (j) { cb(j || {}); })
      .catch(function () { cb({}); });
  }

  // 探测访客 cookie 是否有效（ navigate 一个空请求最省事，但会污染会话；
  // 直接访问受保护端点，401 = 未通过）。
  function probeAuth(cb) {
    fetch('/api/v1/browser/navigate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ probe: true }) // url 为空会被 400 拒绝，仍能区分鉴权
    }).then(function (r) {
      cb(r.status !== 401);
    }).catch(function () { cb(false); });
  }

  function showGate() { $('gate').classList.remove('hidden'); }
  function showChrome() {
    $('chrome').classList.remove('hidden');
    if (resumeId) {
      var id = resumeId;
      resumeId = null;
      history.replaceState(null, '', location.pathname); // 防刷新重复触发
      fetch('/api/v1/history/' + encodeURIComponent(id) + '/resume', { method: 'POST' })
        .then(function (r) {
          if (!r.ok) throw new Error('HTTP ' + r.status);
          return r.json();
        })
        .then(function (j) {
          // 原样展示快照（重放会话种子页，不调 LLM）；
          // 用户在页内交互时才带着种子上下文生成下一页。
          showResumedPage(j.session_id, j.url, id);
        })
        .catch(function () { /* 快照不存在等：留在起始页 */ });
    }
  }

  // 以「重放」方式展示恢复页：快照即会话第 0 页，流式输出存储的 HTML。
  function showResumedPage(sessId, url, histId) {
    lockUI();
    fetch('/api/v1/browser/replay', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessId, index: 0 })
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (resp) {
      state.sessionId = sessId;
      state.currentUrl = url;
      state.currentHistoryId = histId || '';
      $('tab-title').textContent = url;
      $('url-input').value = url;
      swapIframe(resp.stream_url);
      state.history = [{ url: url, pageIndex: 0 }];
      state.historyPos = 0;
      $('start-page').classList.add('hidden');
    }).catch(function (err) {
      unlockUI();
      alert('恢复失败：' + err.message);
    });
  }

  // ---------- 密码门 ----------
  $('gate-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var pw = $('gate-password').value;
    if (!pw) return;
    fetch('/api/v1/auth/site', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pw })
    }).then(function (r) {
      if (r.ok) {
        $('gate').classList.add('hidden');
        showChrome();
      } else {
        $('gate-error').classList.remove('hidden');
      }
    }).catch(function () { $('gate-error').classList.remove('hidden'); });
  });

  // ---------- UI 锁 ----------
  function lockUI() {
    if (state.generating) return;
    state.generating = true;
    $('loading-text').textContent = state.loadingText;
    $('loading-mask').classList.remove('hidden');
    $('url-input').disabled = true;
    $('btn-go').disabled = true;
    $('btn-back').disabled = true;
    $('btn-fwd').disabled = true;
    $('btn-reload').disabled = true;
  }

  function unlockUI() {
    state.generating = false;
    $('loading-mask').classList.add('hidden');
    $('url-input').disabled = false;
    $('btn-go').disabled = false;
    refreshNavButtons();
  }

  function refreshNavButtons() {
    $('btn-back').disabled = state.generating || state.historyPos <= 0;
    $('btn-fwd').disabled = state.generating || state.historyPos >= state.history.length - 1;
    $('btn-reload').disabled = state.generating || state.historyPos < 0;
    $('btn-share').disabled = state.generating || !state.currentHistoryId;
  }

  // ---------- 导航主流程 ----------
  function navigateTo(url, action) {
    if (state.generating) return; // 生成期间禁止一切新导航
    if (!url) return;
    lockUI();
    state.lastNavUrl = url;
    state.lastAction = action || null;

    var body = { url: url, action: action || undefined };
    if (state.sessionId) body.session_id = state.sessionId;

    fetch('/api/v1/browser/navigate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }).then(function (r) {
      if (!r.ok) return r.json().then(function (j) { throw new Error(j.error || ('HTTP ' + r.status)); });
      return r.json();
    }).then(function (resp) {
      state.sessionId = resp.session_id;
      state.currentUrl = url;
      state.currentHistoryId = resp.history_id || '';
      $('tab-title').textContent = url;
      $('url-input').value = url;
      swapIframe(resp.stream_url);
      // 导航历史（仅真实生成计入；action 导航同样计入）。
      state.history = state.history.slice(0, state.historyPos + 1);
      state.history.push({ url: url, pageIndex: resp.page_index });
      state.historyPos = state.history.length - 1;
      $('start-page').classList.add('hidden');
    }).catch(function (err) {
      unlockUI();
      alert('导航失败：' + err.message);
    });
  }

  // ---------- iframe 生命周期 ----------
  // 先设 src 再插入 DOM：规避 about:blank 的 onload 竞态；
  // 每次导航重建 iframe 元素，彻底清掉旧页面全局 JS 状态。
  function swapIframe(src) {
    if (state.currentFrame) {
      state.currentFrame.remove();
      state.currentFrame = null;
    }
    var f = document.createElement('iframe');
    f.setAttribute('sandbox', 'allow-scripts allow-forms');
    f.setAttribute('referrerpolicy', 'no-referrer');
    f.addEventListener('load', function () {
      // 流结束（含外部脚本执行完）后解锁。
      if (state.generating) unlockUI();
    });
    f.addEventListener('error', function () {
      if (state.generating) unlockUI();
    });
    f.src = src;
    state.currentFrame = f;
    $('viewport').appendChild(f);
  }

  // 取消：移除 iframe 会中断流请求 → 服务端 r.Context() 取消 → 上游 LLM 级联取消。
  $('btn-cancel').addEventListener('click', function () {
    if (state.currentFrame) {
      state.currentFrame.remove();
      state.currentFrame = null;
    }
    unlockUI();
  });

  // ---------- bridge 消息闭环 ----------
  window.addEventListener('message', function (e) {
    var d = e.data;
    if (!d || d.__magic !== 'navigate') return;
    // 深度交互：拼成站内路径，保持域名上下文。
    var base = state.currentUrl || '';
    var target = resolveUrl(base, d.url);
    navigateTo(target, d.action || { type: 'click' });
  });

  // 把 bridge 给的相对路径拼到当前域名下。
  function resolveUrl(base, href) {
    if (!href) return base;
    if (/^[a-z][a-z0-9+.-]*:\/\//i.test(href) || /^[a-z][a-z0-9+.-]*:/i.test(href)) return href;
    var host = base.split('/')[0] || 'site.magic';
    if (href.charAt(0) === '/') return host + href;
    return base.replace(/\/[^/]*$/, '') + '/' + href;
  }

  // ---------- 地址栏 / 按钮 ----------
  $('nav-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var u = $('url-input').value.trim();
    if (u) navigateTo(u, null);
  });

  $('btn-back').addEventListener('click', function () { replay(-1); });
  $('btn-fwd').addEventListener('click', function () { replay(1); });

  function replay(delta) {
    var pos = state.historyPos + delta;
    if (pos < 0 || pos >= state.history.length || state.generating) return;
    state.historyPos = pos;
    var entry = state.history[pos];
    lockUI();
    fetch('/api/v1/browser/replay', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: state.sessionId, index: entry.pageIndex })
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (resp) {
      state.currentUrl = entry.url;
      state.currentHistoryId = ''; // 回放页的分享请走后台历史/原分享链接
      $('tab-title').textContent = entry.url;
      $('url-input').value = entry.url;
      swapIframe(resp.stream_url);
    }).catch(function (err) {
      unlockUI();
      alert('回放失败：' + err.message);
    });
  }

  $('btn-reload').addEventListener('click', function () {
    if (state.lastNavUrl && !state.generating) {
      navigateTo(state.lastNavUrl, null); // 重新生成当前页
    }
  });

  // ---------- 分享当前页 ----------
  // currentHistoryId 在 navigate 响应即返回，但仅在流完成落盘后才真正可访问；
  // 生成期间按钮禁用（lockUI/refreshNavButtons），故点击时快照必已存在。
  $('btn-share').addEventListener('click', function () {
    if (!state.currentHistoryId || state.generating) return;
    var url = location.origin + '/s/' + encodeURIComponent(state.currentHistoryId);
    copyText(url, function (ok) {
      var btn = $('btn-share');
      if (ok) {
        btn.classList.add('shared');
        setTimeout(function () { btn.classList.remove('shared'); }, 1200);
      } else {
        prompt('复制下面的分享链接：', url);
      }
    });
  });

  function copyText(text, cb) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { cb(true); }, function () { fallbackCopy(text, cb); });
    } else {
      fallbackCopy(text, cb);
    }
  }
  function fallbackCopy(text, cb) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { /* 忽略 */ }
    document.body.removeChild(ta);
    cb(ok);
  }

  // 起始页示例按钮。
  Array.prototype.forEach.call(document.querySelectorAll('.start-samples button'), function (b) {
    b.addEventListener('click', function () { navigateTo(b.getAttribute('data-url'), null); });
  });

  // ---------- 全屏 ----------
  // 按钮与 F11 之外，还支持双击标签栏切换（浏览器惯例）。
  function toggleFullscreen() {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(function () {});
    } else {
      document.documentElement.requestFullscreen().catch(function () {});
    }
  }
  $('btn-fullscreen').addEventListener('click', toggleFullscreen);
  document.querySelector('.titlebar').addEventListener('dblclick', function (e) {
    if (e.target.closest('#url-input') || e.target.closest('button')) return;
    toggleFullscreen();
  });
  // F11 原生行为由浏览器处理；这里补一个 F 键快捷方式（输入框聚焦时除外）。
  document.addEventListener('keydown', function (e) {
    if (e.key === 'f' || e.key === 'F') {
      if (e.target === $('url-input') || e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;
      e.preventDefault();
      toggleFullscreen();
    }
  });
})();
