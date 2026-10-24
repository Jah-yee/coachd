// coachd runner page. Plain ES2020, no dependencies, no inline handlers
// (the CSP forbids them): listeners attach to [data-action] elements.
"use strict";

// The enrolment token travels in the URL fragment, so it never reaches a
// server or Cloudflare log. Take it and strip it from the address bar and
// history before anything else runs.
const enrollToken = (() => {
  if (location.pathname !== "/enroll") {
    return "";
  }
  const token = location.hash.length > 1 ? location.hash.slice(1) : "";
  history.replaceState(null, "", "/enroll");
  return token;
})();

// base64url (no padding) <-> ArrayBuffer.
function b64urlToBuffer(s) {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    bytes[i] = bin.charCodeAt(i);
  }
  return bytes.buffer;
}

function bufferToB64url(buf) {
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (let i = 0; i < bytes.length; i++) {
    bin += String.fromCharCode(bytes[i]);
  }
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function descriptorsToBuffers(list) {
  return (list || []).map((c) => Object.assign({}, c, { id: b64urlToBuffer(c.id) }));
}

// creationOptions turns the server's {"publicKey": {...}} into what
// navigator.credentials.create takes.
function creationOptions(options) {
  const pk = Object.assign({}, options.publicKey);
  pk.challenge = b64urlToBuffer(pk.challenge);
  pk.user = Object.assign({}, pk.user, { id: b64urlToBuffer(pk.user.id) });
  if (pk.excludeCredentials) {
    pk.excludeCredentials = descriptorsToBuffers(pk.excludeCredentials);
  }
  return { publicKey: pk };
}

// requestOptions does the same for navigator.credentials.get.
function requestOptions(options) {
  const pk = Object.assign({}, options.publicKey);
  pk.challenge = b64urlToBuffer(pk.challenge);
  if (pk.allowCredentials) {
    pk.allowCredentials = descriptorsToBuffers(pk.allowCredentials);
  }
  return { publicKey: pk };
}

// credentialJSON serializes a PublicKeyCredential with base64url fields.
function credentialJSON(cred) {
  const r = cred.response;
  const response = { clientDataJSON: bufferToB64url(r.clientDataJSON) };
  if (r.attestationObject) {
    response.attestationObject = bufferToB64url(r.attestationObject);
    if (typeof r.getTransports === "function") {
      response.transports = r.getTransports();
    }
  }
  if (r.authenticatorData) {
    response.authenticatorData = bufferToB64url(r.authenticatorData);
  }
  if (r.signature) {
    response.signature = bufferToB64url(r.signature);
  }
  if (r.userHandle) {
    response.userHandle = bufferToB64url(r.userHandle);
  }
  const out = {
    id: cred.id,
    rawId: bufferToB64url(cred.rawId),
    type: cred.type,
    response: response,
    clientExtensionResults:
      typeof cred.getClientExtensionResults === "function" ? cred.getClientExtensionResults() : {},
  };
  if (cred.authenticatorAttachment) {
    out.authenticatorAttachment = cred.authenticatorAttachment;
  }
  return out;
}

function showError(message) {
  const el = document.getElementById("alert");
  if (el) {
    el.textContent = message;
    el.hidden = false;
  }
}

function clearError() {
  const el = document.getElementById("alert");
  if (el) {
    el.textContent = "";
    el.hidden = true;
  }
}

// postJSON posts body as JSON and returns the parsed reply. A non-2xx reply
// throws with the server's "error" message.
async function postJSON(url, body) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    credentials: "same-origin",
  });
  let data = null;
  try {
    data = await res.json();
  } catch (_) {
    data = null;
  }
  if (!res.ok) {
    throw new Error((data && data.error) || "request failed (" + res.status + ")");
  }
  return data || {};
}

// assertAndPost runs navigator.credentials.get with the server's options
// and posts {ceremony, credential} to url. Login and approve both use it.
async function assertAndPost(options, ceremony, url) {
  const cred = await navigator.credentials.get(requestOptions(options));
  if (!cred) {
    throw new Error("no passkey was used");
  }
  return postJSON(url, { ceremony: ceremony, credential: credentialJSON(cred) });
}

function describe(err) {
  if (err && err.name === "NotAllowedError") {
    return "The passkey prompt was cancelled or timed out. Try again.";
  }
  if (err && err.name === "InvalidStateError") {
    return "This passkey is already enrolled.";
  }
  return (err && err.message) || "Something went wrong.";
}

function passkeysSupported() {
  if (window.PublicKeyCredential && navigator.credentials) {
    return true;
  }
  showError("This browser does not support passkeys.");
  return false;
}

// onClick wires a button: it disables it while fn runs and shows errors.
function onClick(el, fn) {
  el.addEventListener("click", async (ev) => {
    ev.preventDefault();
    if (!passkeysSupported()) {
      return;
    }
    clearError();
    el.disabled = true;
    try {
      await fn();
    } catch (err) {
      showError(describe(err));
      el.disabled = false;
    }
  });
}

// localPath returns next when it is a path on this origin, else "/". The
// server already validated data-next (safeNext); this is a second check.
function localPath(next) {
  if (typeof next !== "string" || !next.startsWith("/") || next.startsWith("//") || next.includes("\\")) {
    return "/";
  }
  return next;
}

// A login button carries data-next, the server-validated page to land on.
function wireLogin() {
  document.querySelectorAll('[data-action="login"]').forEach((btn) => {
    onClick(btn, async () => {
      const begin = await postJSON("/login/begin", {});
      await assertAndPost(begin.options, begin.ceremony, "/login/finish");
      location.assign(localPath(btn.dataset.next));
    });
  });
}

// An approve button carries data-url, data-ceremony and data-options.
function wireApprove() {
  document.querySelectorAll('[data-action="approve"]').forEach((btn) => {
    onClick(btn, async () => {
      await assertAndPost(JSON.parse(btn.dataset.options), btn.dataset.ceremony, btn.dataset.url);
      location.reload();
    });
  });
}

// consentFlags reads the consent checkboxes next to a button.
function consentFlags(btn) {
  const form = btn.closest("form");
  const box = (name) => !!(form && form.elements[name] && form.elements[name].checked);
  return { write: box("write"), recognise: box("recognise") };
}

// The consent buttons carry data-request. Only coachd's own JSON may set
// the address the browser goes to.
function wireConsent() {
  document.querySelectorAll('[data-action="consent-approve"]').forEach((btn) => {
    onClick(btn, async () => {
      const flags = consentFlags(btn);
      const begin = await postJSON("/oauth/authorize/begin", {
        request: btn.dataset.request,
        write: flags.write,
        recognise: flags.recognise,
      });
      const body = await assertAndPost(begin.options, begin.ceremony, "/oauth/authorize/finish");
      location.assign(body.redirect);
    });
  });
  document.querySelectorAll('[data-action="consent-deny"]').forEach((btn) => {
    onClick(btn, async () => {
      const body = await postJSON("/oauth/authorize/deny", { request: btn.dataset.request });
      location.assign(body.redirect);
    });
  });
}

function wireEnroll() {
  const form = document.querySelector('[data-action="enroll"]');
  if (!form) {
    return;
  }
  const button = form.querySelector('button[type="submit"]');
  if (!enrollToken) {
    showError("No enrolment token in this address: open the enrolment link again " +
      "(if Cloudflare Access asked you to sign in first, that dropped the token).");
    if (button) {
      button.disabled = true;
    }
    return;
  }
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (!passkeysSupported()) {
      return;
    }
    clearError();
    if (button) {
      button.disabled = true;
    }
    try {
      const begin = await postJSON("/enroll/begin", { token: enrollToken });
      const cred = await navigator.credentials.create(creationOptions(begin.options));
      if (!cred) {
        throw new Error("no passkey was created");
      }
      await postJSON("/enroll/finish", {
        ceremony: begin.ceremony,
        token: enrollToken,
        label: form.elements.label.value,
        credential: credentialJSON(cred),
      });
      location.assign("/");
    } catch (err) {
      showError(describe(err));
      if (button) {
        button.disabled = false;
      }
    }
  });
}

wireLogin();
wireApprove();
wireConsent();
wireEnroll();
