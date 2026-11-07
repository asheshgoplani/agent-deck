// connect.js -- the standalone /connect page: tick scopes, get one block,
// Copy, Test. Talks to the owner endpoints /api/connect/*. The owner token
// (if the server has one) comes from ?token= once, is kept in memory only
// and stripped from the address bar; the token cookie covers later visits.
;(function () {
  'use strict'

  var ownerToken = ''
  try {
    var params = new URLSearchParams(window.location.search)
    ownerToken = params.get('token') || ''
    if (ownerToken) {
      params.delete('token')
      var q = params.toString()
      window.history.replaceState(null, '', window.location.pathname + (q ? '?' + q : ''))
    }
  } catch (e) { /* old browser: cookie auth still works */ }

  var $ = function (id) { return document.getElementById(id) }
  var lastToken = ''
  var lastBlock = ''

  function api(path, body) {
    var headers = { 'Accept': 'application/json' }
    if (ownerToken) headers['Authorization'] = 'Bearer ' + ownerToken
    var opts = { headers: headers, credentials: 'same-origin' }
    if (body !== undefined) {
      opts.method = 'POST'
      headers['Content-Type'] = 'application/json'
      opts.body = JSON.stringify(body)
    }
    return fetch(path, opts).then(function (r) {
      return r.json().catch(function () { return {} }).then(function (j) {
        if (!r.ok) {
          var msg = (j && j.error && (j.error.message || j.error)) || ('HTTP ' + r.status)
          if (r.status === 401) msg = 'Not signed in: open this page from the agent-deck web link (with ?token=).'
          throw new Error(msg)
        }
        return j
      })
    })
  }

  function setStatus(el, text, cls) {
    el.textContent = text
    el.className = 'status' + (cls ? ' ' + cls : '')
  }

  function copyText(text, statusEl) {
    var done = function () { setStatus(statusEl, 'Copied.', 'ok') }
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text, statusEl) })
    } else {
      fallbackCopy(text, statusEl)
    }
  }

  function fallbackCopy(text, statusEl) {
    var ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    var ok = false
    try { ok = document.execCommand('copy') } catch (e) { ok = false }
    document.body.removeChild(ta)
    setStatus(statusEl, ok ? 'Copied.' : 'Select the text and copy it by hand.', ok ? 'ok' : 'bad')
  }

  function renderTokens(tokens) {
    var box = $('tokens')
    box.textContent = ''
    if (!tokens || tokens.length === 0) {
      box.textContent = 'None yet.'
      return
    }
    var table = document.createElement('table')
    var head = table.insertRow()
    ;['Name', 'Assistant', 'Scopes'].forEach(function (h) {
      var th = document.createElement('th')
      th.textContent = h
      head.appendChild(th)
    })
    tokens.forEach(function (t) {
      var row = table.insertRow()
      row.insertCell().textContent = t.name
      row.insertCell().textContent = t.assistant
      row.insertCell().textContent = (t.scopes || []).join(', ')
    })
    box.appendChild(table)
  }

  function selectedScopes() {
    var out = []
    document.querySelectorAll('#scopes input[type=checkbox]').forEach(function (cb) {
      if (cb.checked) out.push(cb.value)
    })
    return out
  }

  function load() {
    api('/api/connect/assistants').then(function (info) {
      $('setup-line').textContent = 'Read ' + info.setup_prompt + ' and follow it to connect yourself to agent-deck on my machine. Ask me before each step that changes anything.'

      var scopesBox = $('scopes')
      scopesBox.textContent = ''
      ;(info.scopes || []).forEach(function (sc) {
        var label = document.createElement('label')
        var cb = document.createElement('input')
        cb.type = 'checkbox'
        cb.value = sc.id
        cb.checked = !!sc.default
        if (sc.id !== 'read' && !info.write_scopes) {
          cb.disabled = true
          cb.title = 'This server is read-only'
        }
        var text = document.createElement('span')
        var b = document.createElement('strong')
        b.textContent = sc.id
        text.appendChild(b)
        text.appendChild(document.createTextNode(': ' + sc.description))
        label.appendChild(cb)
        label.appendChild(text)
        scopesBox.appendChild(label)
      })

      var kind = $('kind')
      kind.textContent = ''
      ;(info.assistants || []).forEach(function (k) {
        var opt = document.createElement('option')
        opt.value = k.id
        opt.textContent = k.label
        if (k.id === info.default) opt.selected = true
        kind.appendChild(opt)
      })
      renderTokens(info.tokens)
    }).catch(function (err) {
      setStatus($('status'), err.message, 'bad')
      $('tokens').textContent = ''
      $('make').disabled = true
    })
  }

  $('copy-setup').addEventListener('click', function () {
    copyText($('setup-line').textContent, $('setup-status'))
  })

  $('make').addEventListener('click', function () {
    var statusEl = $('status')
    setStatus(statusEl, 'Creating token...')
    $('make').disabled = true
    api('/api/connect/tokens', {
      assistant: $('kind').value,
      name: $('name').value.trim(),
      scopes: selectedScopes(),
      url: window.location.origin
    }).then(function (conn) {
      lastToken = conn.token
      lastBlock = conn.block
      var pre = $('block')
      pre.textContent = conn.block
      pre.hidden = false
      $('copy').disabled = false
      $('test').disabled = false
      setStatus(statusEl, 'Token "' + conn.name + '" ready (' + conn.scopes.join(', ') + ').', 'ok')
      return api('/api/connect/assistants')
    }).then(function (info) {
      if (info) renderTokens(info.tokens)
    }).catch(function (err) {
      setStatus(statusEl, err.message, 'bad')
    }).then(function () {
      $('make').disabled = false
    })
  })

  $('copy').addEventListener('click', function () {
    if (lastBlock) copyText(lastBlock, $('status'))
  })

  $('test').addEventListener('click', function () {
    var statusEl = $('status')
    if (!lastToken) return
    setStatus(statusEl, 'Testing...')
    api('/api/connect/test', { token: lastToken }).then(function (res) {
      if (res.ok) {
        setStatus(statusEl, 'Works: list_sessions sees ' + res.sessions + ' session(s).', 'ok')
      } else {
        setStatus(statusEl, 'Test failed: ' + res.error, 'bad')
      }
    }).catch(function (err) {
      setStatus(statusEl, 'Test failed: ' + err.message, 'bad')
    })
  })

  load()
})()
