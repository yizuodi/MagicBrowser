// Magic Browser 管理后台：登录 → 拉取配置 → 部分更新保存。
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };

  function toast(msg, isErr) {
    var t = $('toast');
    t.textContent = msg;
    t.className = isErr ? 'err show' : 'show';
    setTimeout(function () { t.className = ''; }, 2200);
  }

  // ---------- 登录 ----------
  $('login-form').addEventListener('submit', function (e) {
    e.preventDefault();
    fetch('/api/v1/admin/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: $('login-password').value })
    }).then(function (r) {
      if (r.ok) {
        enterPanel();
      } else {
        $('login-error').textContent = '管理员密码不正确';
      }
    }).catch(function () { $('login-error').textContent = '网络错误'; });
  });

  $('btn-logout').addEventListener('click', function (e) {
    e.preventDefault();
    fetch('/api/v1/admin/logout', { method: 'POST' }).then(function () { location.reload(); });
  });

  function enterPanel() {
    $('login').classList.add('hidden');
    $('panel').classList.remove('hidden');
    loadConfig();
    loadHistory();
  }

  // ---------- 配置加载/保存 ----------
  function loadConfig() {
    fetch('/api/v1/admin/config').then(function (r) {
      if (r.status === 401) return Promise.reject(new Error('未登录'));
      return r.json();
    }).then(function (c) {
      $('f-provider').value = c.llm_provider_url || '';
      $('f-model').value = c.llm_model_name || '';
      $('apikey-hint').textContent = c.has_api_key ? '✓ 已设置（留空保留原值）' : '尚未设置';
      $('f-apikey').placeholder = c.has_api_key ? '已设置（留空保留）' : '';
      $('f-loading').value = c.loading_text || '';
      $('f-sysprompt').value = c.system_prompt || '';
      $('f-threshold').value = c.context_compress_threshold || 200000;
      $('f-cdn').value = c.cdn_base_url || '';
      $('f-timeout').value = c.gen_timeout_sec || 120;
      $('f-gate-on').checked = !!c.site_gate_enabled;
      $('site-pw-field').style.display = c.site_gate_enabled ? '' : 'none';
    }).catch(function (err) {
      toast(err.message, true);
    });
  }

  $('f-gate-on').addEventListener('change', function () {
    $('site-pw-field').style.display = $('f-gate-on').checked ? '' : 'none';
  });

  $('btn-reload-config').addEventListener('click', loadConfig);

  $('config-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var payload = {
      llm_provider_url: $('f-provider').value.trim(),
      llm_model_name: $('f-model').value.trim(),
      loading_text: $('f-loading').value,
      system_prompt: $('f-sysprompt').value,
      cdn_base_url: $('f-cdn').value.trim(),
      context_compress_threshold: parseInt($('f-threshold').value, 10) || 0,
      gen_timeout_sec: parseInt($('f-timeout').value, 10) || 0
    };
    var key = $('f-apikey').value;
    if (key) payload.llm_api_key = key;                       // 空串=保留
    var adminPw = $('f-adminpw').value;
    if (adminPw) payload.admin_password = adminPw;
    if ($('f-gate-on').checked) {
      var sitePw = $('f-sitepw').value;
      if (sitePw) payload.site_password = sitePw;
    } else {
      payload.disable_site_gate = true;                        // 关闭密码门
    }

    fetch('/api/v1/admin/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function (r) {
      if (r.status === 401) return Promise.reject(new Error('登录已过期'));
      if (!r.ok) return r.json().then(function (j) { throw new Error(j.error || 'HTTP ' + r.status); });
      return r.json();
    }).then(function () {
      toast('已保存 ✓');
      $('f-apikey').value = '';
      $('f-adminpw').value = '';
      $('f-sitepw').value = '';
      loadConfig();
    }).catch(function (err) { toast('保存失败：' + err.message, true); });
  });

  // 刷新时探测是否已登录。
  fetch('/api/v1/admin/config').then(function (r) {
    if (r.ok) enterPanel();
  }).catch(function () { /* 忽略 */ });

  // ---------- 页面历史 ----------
  var hist = { page: 0, limit: 20, total: 0 };

  function fmtTime(unix) {
    var d = new Date(unix * 1000);
    function p(n) { return n < 10 ? '0' + n : n; }
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function fmtSize(n) {
    if (n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
    return n + ' B';
  }

  function loadHistory() {
    fetch('/api/v1/admin/history?limit=' + hist.limit + '&offset=' + (hist.page * hist.limit))
      .then(function (r) {
        if (r.status === 401) return Promise.reject(new Error('登录已过期'));
        return r.json();
      })
      .then(renderHistory)
      .catch(function (err) { toast(err.message, true); });
  }

  function renderHistory(j) {
    hist.total = j.total || 0;
    $('hist-total').textContent = hist.total ? '（共 ' + hist.total + ' 条）' : '';
    var tb = $('hist-body');
    tb.innerHTML = '';
    if (!j.items || !j.items.length) {
      tb.innerHTML = '<tr><td colspan="5" style="color:var(--dim)">暂无历史记录</td></tr>';
    } else {
      j.items.forEach(function (m) {
        var tr = document.createElement('tr');
        var sess = (m.session_id || '').replace(/^sess_/, '');
        if (sess.length > 10) sess = sess.slice(0, 10) + '…';
        tr.innerHTML =
          '<td>' + fmtTime(m.created_at) + '</td>' +
          '<td class="url" title="' + esc(m.url) + '">' + esc(m.url) + '</td>' +
          '<td class="sess">' + esc(sess) + '</td>' +
          '<td>' + fmtSize(m.size) + '</td>' +
          '<td>' +
            '<button class="row-btn" data-act="preview" data-id="' + m.id + '">预览</button>' +
            '<button class="row-btn" data-act="share" data-id="' + m.id + '">分享</button>' +
            '<button class="row-btn" data-act="resume" data-id="' + m.id + '">继续</button>' +
            '<button class="row-btn danger" data-act="delete" data-id="' + m.id + '">删除</button>' +
          '</td>';
        tb.appendChild(tr);
      });
    }
    var pages = Math.max(1, Math.ceil(hist.total / hist.limit));
    $('hist-page').textContent = (hist.page + 1) + ' / ' + pages + ' 页';
    $('hist-prev').disabled = hist.page <= 0;
    $('hist-next').disabled = hist.page >= pages - 1;
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // 事件委托：单处监听四个操作。
  $('hist-body').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var id = btn.getAttribute('data-id');
    var act = btn.getAttribute('data-act');
    if (act === 'preview') {
      window.open('/s/' + encodeURIComponent(id), '_blank');
    } else if (act === 'share') {
      copyShare(location.origin + '/s/' + encodeURIComponent(id));
    } else if (act === 'resume') {
      window.open('/?resume=' + encodeURIComponent(id), '_blank');
    } else if (act === 'delete') {
      if (!confirm('删除这条历史？')) return;
      fetch('/api/v1/admin/history/' + encodeURIComponent(id), { method: 'DELETE' })
        .then(function (r) {
          if (!r.ok) throw new Error('HTTP ' + r.status);
          toast('已删除');
          loadHistory();
        })
        .catch(function (err) { toast('删除失败：' + err.message, true); });
    }
  });

  function copyShare(url) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(function () {
        toast('分享链接已复制');
      }, function () { fallbackCopy(url); });
    } else {
      fallbackCopy(url);
    }
  }
  function fallbackCopy(url) {
    var ta = document.createElement('textarea');
    ta.value = url;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { /* 忽略 */ }
    document.body.removeChild(ta);
    if (ok) toast('分享链接已复制');
    else prompt('复制下面的分享链接：', url);
  }

  $('btn-hist-refresh').addEventListener('click', loadHistory);
  $('hist-prev').addEventListener('click', function () {
    if (hist.page > 0) { hist.page--; loadHistory(); }
  });
  $('hist-next').addEventListener('click', function () {
    if ((hist.page + 1) * hist.limit < hist.total) { hist.page++; loadHistory(); }
  });
  $('btn-hist-clear').addEventListener('click', function () {
    if (!hist.total) { toast('没有可清空的历史'); return; }
    if (!confirm('确定清空全部 ' + hist.total + ' 条历史？此操作不可恢复。')) return;
    fetch('/api/v1/admin/history', { method: 'DELETE' })
      .then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        hist.page = 0;
        toast('已清空');
        loadHistory();
      })
      .catch(function (err) { toast('清空失败：' + err.message, true); });
  });
})();
