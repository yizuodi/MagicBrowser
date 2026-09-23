// Magic Browser bridge：注入到每个生成页面最前方。
// 在 sandbox iframe（无 allow-same-origin）内运行，深度交互经 postMessage 交给父页。
(function () {
  'use strict';

  function nav(url, action) {
    try {
      parent.postMessage({ __magic: 'navigate', url: url, action: action }, '*');
    } catch (e) { /* 父页不存在时静默 */ }
  }

  function describe(el) {
    if (!el) return '';
    var d = el.getAttribute('data-magic-nav') || el.id || el.className || el.tagName || '';
    return String(d).slice(0, 64);
  }

  // 捕获阶段的点击拦截：data-magic-nav 标记元素与普通链接。
  document.addEventListener('click', function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey) return;
    var el = e.target.closest('[data-magic-nav]');
    if (el) {
      e.preventDefault();
      var url = el.getAttribute('data-magic-url') || el.getAttribute('href') || '';
      nav(url, { type: 'click', element: describe(el) });
      return;
    }
    var a = e.target.closest('a[href]');
    if (a) {
      var href = a.getAttribute('href') || '';
      if (href === '' || href.charAt(0) === '#' || href.indexOf('javascript:') === 0) return;
      e.preventDefault();
      nav(href, { type: 'link', element: describe(a) });
    }
  }, true);

  // 表单提交拦截：data-magic-local 的表单留给页面自己的 JS 处理。
  document.addEventListener('submit', function (e) {
    var f = e.target;
    if (!f || f.hasAttribute('data-magic-local')) return;
    e.preventDefault();
    var data = {};
    try {
      new FormData(f).forEach(function (v, k) { data[k] = String(v).slice(0, 200); });
    } catch (err) { /* 非法表单忽略 */ }
    nav(f.getAttribute('action') || location.pathname || '', {
      type: 'submit',
      element: (f.id || 'form').slice(0, 64),
      form: data
    });
  }, true);
})();
