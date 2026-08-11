(function () {
  var history = []; // {role, content} — trimmed client-side, server trims again as a floor
  var MAX_HISTORY = 6;

  var loginView = document.getElementById("login-view");
  var chatView = document.getElementById("chat-view");
  var messagesEl = document.getElementById("messages");
  var form = document.getElementById("chat-form");
  var input = document.getElementById("input");
  var sendBtn = document.getElementById("send");
  var statusEl = document.getElementById("status");

  var tabsEl = document.getElementById("tabs");
  var chatTabBtn = document.getElementById("chat-tab-btn");
  var adminTabBtn = document.getElementById("admin-tab-btn");
  var adminView = document.getElementById("admin-view");
  var adminErrorEl = document.getElementById("admin-error");
  var usersTableBody = document.getElementById("users-table-body");
  var addUserForm = document.getElementById("add-user-form");
  var addUsernameInput = document.getElementById("add-username");
  var addRoleSelect = document.getElementById("add-role");
  var assumeRoleBtn = document.getElementById("assume-role-btn");
  var assumeRoleResult = document.getElementById("assume-role-result");
  var assumeRoleDuration = document.getElementById("assume-role-duration");

  var homeView = document.getElementById("home-view");
  var profileAvatarEl = document.getElementById("profile-avatar");
  var profileNameEl = document.getElementById("profile-name");
  var profileUsernameEl = document.getElementById("profile-username");
  var profileEmailEl = document.getElementById("profile-email");
  var profileRoleEl = document.getElementById("profile-role");
  var profileOrgsRow = document.getElementById("profile-orgs-row");
  var profileOrgsEl = document.getElementById("profile-orgs");
  var homeTabBtn = document.getElementById("home-tab-btn");
  var homeAdminCard = document.getElementById("home-admin-card");
  var awsView = document.getElementById("aws-view");
  var awsTabBtn = document.getElementById("aws-tab-btn");
  var awsErrorEl = document.getElementById("aws-error");
  var awsLfsBtn = document.getElementById("aws-lfs-btn");
  var awsLfsResult = document.getElementById("aws-lfs-result");
  var awsLfsDeleteBtn = document.getElementById("aws-lfs-delete-btn");
  var awsConsoleBtn = document.getElementById("aws-console-btn");
  var awsConsoleResult = document.getElementById("aws-console-result");
  var awsStsBtn = document.getElementById("aws-sts-btn");
  var awsStsResult = document.getElementById("aws-sts-result");

  function escapeHtml(s) {
    var div = document.createElement("div");
    div.textContent = s;
    return div.innerHTML;
  }

  function addMessage(role, text, citations) {
    var el = document.createElement("div");
    el.className = "msg " + role;
    el.innerHTML = escapeHtml(text).replace(/\n/g, "<br>");

    if (citations && citations.length) {
      var cWrap = document.createElement("div");
      cWrap.className = "citations";
      citations.forEach(function (c) {
        var tag = document.createElement("span");
        tag.className = "citation";
        tag.textContent = c.file + ":" + c.startLine + "-" + c.endLine;
        cWrap.appendChild(tag);
      });
      el.appendChild(cWrap);
    }

    messagesEl.appendChild(el);
    messagesEl.scrollTop = messagesEl.scrollHeight;
    return el;
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var message = input.value.trim();
    if (!message) return;

    addMessage("user", message);
    input.value = "";
    sendBtn.disabled = true;
    statusEl.textContent = "Thinking…";

    fetch("/api/v1/chat", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message: message, history: history }),
    })
      .then(function (res) {
        if (!res.ok) throw new Error("Request failed (" + res.status + ")");
        return res.json();
      })
      .then(function (data) {
        addMessage("assistant", data.answer, data.citations);
        history.push({ role: "user", content: message });
        history.push({ role: "assistant", content: data.answer });
        if (history.length > MAX_HISTORY) {
          history = history.slice(history.length - MAX_HISTORY);
        }
      })
      .catch(function (err) {
        addMessage("error", "Something went wrong: " + err.message);
      })
      .finally(function () {
        sendBtn.disabled = false;
        statusEl.textContent = "";
        input.focus();
      });
  });

  input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  function showView(name) {
    homeView.style.display = name === "home" ? "flex" : "none";
    chatView.style.display = name === "chat" ? "flex" : "none";
    awsView.style.display = name === "aws" ? "flex" : "none";
    adminView.style.display = name === "admin" ? "flex" : "none";
    homeTabBtn.classList.toggle("active", name === "home");
    chatTabBtn.classList.toggle("active", name === "chat");
    awsTabBtn.classList.toggle("active", name === "aws");
    adminTabBtn.classList.toggle("active", name === "admin");
    if (name === "admin") loadUsers();
    if (name === "chat") input.focus();
  }

  homeTabBtn.addEventListener("click", function () { showView("home"); });
  chatTabBtn.addEventListener("click", function () { showView("chat"); });
  awsTabBtn.addEventListener("click", function () { showView("aws"); });
  adminTabBtn.addEventListener("click", function () { showView("admin"); });

  Array.prototype.forEach.call(document.querySelectorAll(".home-card"), function (card) {
    card.addEventListener("click", function () { showView(card.getAttribute("data-view")); });
  });

  function showAdminError(msg) {
    adminErrorEl.textContent = msg;
    adminErrorEl.style.display = msg ? "block" : "none";
  }

  function apiErrorMessage(res, fallback) {
    return res.json().then(function (body) {
      return body && body.message ? body.message : fallback;
    }).catch(function () {
      return fallback;
    });
  }

  function loadUsers() {
    showAdminError("");
    fetch("/api/v1/admin/users", { credentials: "include" })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to load users").then(function (m) { throw new Error(m); });
        return res.json();
      })
      .then(renderUsers)
      .catch(function (err) { showAdminError(err.message); });
  }

  function renderUsers(list) {
    usersTableBody.innerHTML = "";
    (list || []).forEach(function (u) {
      var tr = document.createElement("tr");

      var usernameTd = document.createElement("td");
      usernameTd.textContent = u.username;
      tr.appendChild(usernameTd);

      var roleTd = document.createElement("td");
      var roleSelect = document.createElement("select");
      ["developer", "admin"].forEach(function (r) {
        var opt = document.createElement("option");
        opt.value = r;
        opt.textContent = r;
        if (r === u.role) opt.selected = true;
        roleSelect.appendChild(opt);
      });
      roleSelect.addEventListener("change", function () {
        changeRole(u.username, roleSelect.value);
      });
      roleTd.appendChild(roleSelect);
      tr.appendChild(roleTd);

      var addedTd = document.createElement("td");
      addedTd.textContent = u.addedAt ? new Date(u.addedAt).toLocaleDateString() : "";
      tr.appendChild(addedTd);

      var actionsTd = document.createElement("td");
      var removeBtn = document.createElement("button");
      removeBtn.type = "button";
      removeBtn.className = "remove-btn";
      removeBtn.textContent = "Remove";
      removeBtn.addEventListener("click", function () { removeUser(u.username); });
      actionsTd.appendChild(removeBtn);
      tr.appendChild(actionsTd);

      usersTableBody.appendChild(tr);
    });
  }

  function changeRole(username, role) {
    showAdminError("");
    fetch("/api/v1/admin/users/" + encodeURIComponent(username), {
      method: "PATCH",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ role: role }),
    })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to change role").then(function (m) { throw new Error(m); });
      })
      .catch(function (err) { showAdminError(err.message); })
      .finally(loadUsers);
  }

  function removeUser(username) {
    if (!confirm("Remove " + username + "'s access?")) return;
    showAdminError("");
    fetch("/api/v1/admin/users/" + encodeURIComponent(username), {
      method: "DELETE",
      credentials: "include",
    })
      .then(function (res) {
        if (!res.ok && res.status !== 204) return apiErrorMessage(res, "Failed to remove user").then(function (m) { throw new Error(m); });
      })
      .catch(function (err) { showAdminError(err.message); })
      .finally(loadUsers);
  }

  addUserForm.addEventListener("submit", function (e) {
    e.preventDefault();
    var username = addUsernameInput.value.trim();
    if (!username) return;
    showAdminError("");
    fetch("/api/v1/admin/users", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: username, role: addRoleSelect.value }),
    })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to add user").then(function (m) { throw new Error(m); });
        addUsernameInput.value = "";
      })
      .catch(function (err) { showAdminError(err.message); })
      .finally(loadUsers);
  });

  assumeRoleBtn.addEventListener("click", function () {
    showAdminError("");
    assumeRoleBtn.disabled = true;
    fetch("/api/v1/admin/assume-role", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ role: "developer", durationSeconds: parseInt(assumeRoleDuration.value, 10) }),
    })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to assume role").then(function (m) { throw new Error(m); });
        return res.json();
      })
      .then(renderAssumeRoleResult)
      .catch(function (err) { showAdminError(err.message); })
      .finally(function () { assumeRoleBtn.disabled = false; });
  });

  function renderAssumeRoleResult(data) {
    assumeRoleResult.innerHTML = "";
    assumeRoleResult.style.display = "flex";

    var expiry = document.createElement("div");
    expiry.className = "field-label";
    expiry.textContent = "Role: " + data.role + " — expires " + new Date(data.expiresAt).toLocaleTimeString();
    assumeRoleResult.appendChild(expiry);

    copyableRow(assumeRoleResult, "Token", data.token);
  }

  // Builds one labeled, read-only, copy-button field row inside a reveal
  // box — shared by the assume-role token and the AWS console/STS results
  // below, since all three are "show a secret once, let them copy it."
  function copyableRow(container, label, value) {
    var wrap = document.createElement("div");

    var labelEl = document.createElement("div");
    labelEl.className = "field-label";
    labelEl.textContent = label;
    wrap.appendChild(labelEl);

    var row = document.createElement("div");
    row.className = "row";

    var input = document.createElement("input");
    input.type = "text";
    input.readOnly = true;
    input.value = value;
    row.appendChild(input);

    var copyBtn = document.createElement("button");
    copyBtn.type = "button";
    copyBtn.textContent = "Copy";
    copyBtn.addEventListener("click", function () {
      input.select();
      var done = function () {
        copyBtn.textContent = "Copied!";
        setTimeout(function () { copyBtn.textContent = "Copy"; }, 1500);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(value).then(done).catch(function () {
          document.execCommand("copy");
          done();
        });
      } else {
        document.execCommand("copy");
        done();
      }
    });
    row.appendChild(copyBtn);

    wrap.appendChild(row);
    container.appendChild(wrap);
  }

  function showAwsError(msg) {
    awsErrorEl.textContent = msg;
    awsErrorEl.style.display = msg ? "block" : "none";
  }

  awsLfsBtn.addEventListener("click", function () {
    showAwsError("");
    awsLfsBtn.disabled = true;
    fetch("/api/v1/aws/lfs-access-key", { method: "POST", credentials: "include" })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to get an LFS access key").then(function (m) { throw new Error(m); });
        return res.json();
      })
      .then(renderLfsResult)
      .catch(function (err) { showAwsError(err.message); })
      .finally(function () { awsLfsBtn.disabled = false; });
  });

  function renderLfsResult(data) {
    awsLfsResult.innerHTML = "";
    awsLfsResult.style.display = "flex";

    if (data.alreadyExists) {
      var note = document.createElement("div");
      note.className = "field-label";
      note.textContent = "You already have an active key (" + data.accessKeyId + "). Use \"Delete LFS Access Key\" above, then request a new one.";
      awsLfsResult.appendChild(note);
      return;
    }

    var note = document.createElement("div");
    note.className = "field-label";
    note.textContent = "Shown once — save this now.";
    awsLfsResult.appendChild(note);
    copyableRow(awsLfsResult, "AWS_ACCESS_KEY_ID", data.accessKeyId);
    copyableRow(awsLfsResult, "AWS_SECRET_ACCESS_KEY", data.secretAccessKey);
  }

  awsLfsDeleteBtn.addEventListener("click", function () {
    if (!confirm("Delete your LFS access key? git operations using it will stop working until you get a new one.")) return;
    showAwsError("");
    awsLfsDeleteBtn.disabled = true;
    fetch("/api/v1/aws/lfs-access-key", { method: "DELETE", credentials: "include" })
      .then(function (res) {
        if (!res.ok && res.status !== 204) return apiErrorMessage(res, "Failed to delete LFS access key").then(function (m) { throw new Error(m); });
        awsLfsResult.innerHTML = "";
        awsLfsResult.style.display = "none";
      })
      .catch(function (err) { showAwsError(err.message); })
      .finally(function () { awsLfsDeleteBtn.disabled = false; });
  });

  awsConsoleBtn.addEventListener("click", function () {
    showAwsError("");
    awsConsoleBtn.disabled = true;
    fetch("/api/v1/aws/console-access", { method: "POST", credentials: "include" })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to get console access").then(function (m) { throw new Error(m); });
        return res.json();
      })
      .then(renderConsoleResult)
      .catch(function (err) { showAwsError(err.message); })
      .finally(function () { awsConsoleBtn.disabled = false; });
  });

  function renderConsoleResult(data) {
    awsConsoleResult.innerHTML = "";
    awsConsoleResult.style.display = "flex";

    if (data.alreadyExisted) {
      var note = document.createElement("div");
      note.className = "field-label";
      note.textContent = "You already have console access — use \"forgot password\" on the AWS sign-in page if you need a reset.";
      awsConsoleResult.appendChild(note);
      return;
    }

    var note = document.createElement("div");
    note.className = "field-label";
    note.textContent = "Shown once — save this now.";
    awsConsoleResult.appendChild(note);
    copyableRow(awsConsoleResult, "Console sign-in URL", data.consoleSignInURL);
    copyableRow(awsConsoleResult, "Username", data.username);
    copyableRow(awsConsoleResult, "Temporary password", data.temporaryPassword);
  }

  awsStsBtn.addEventListener("click", function () {
    showAwsError("");
    awsStsBtn.disabled = true;
    fetch("/api/v1/aws/sts-credentials", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    })
      .then(function (res) {
        if (!res.ok) return apiErrorMessage(res, "Failed to get temporary credentials").then(function (m) { throw new Error(m); });
        return res.json();
      })
      .then(renderStsResult)
      .catch(function (err) { showAwsError(err.message); })
      .finally(function () { awsStsBtn.disabled = false; });
  });

  function renderStsResult(data) {
    awsStsResult.innerHTML = "";
    awsStsResult.style.display = "flex";

    var expiry = document.createElement("div");
    expiry.className = "field-label";
    expiry.textContent = "Expires " + new Date(data.expiration).toLocaleString();
    awsStsResult.appendChild(expiry);

    copyableRow(awsStsResult, "AWS_ACCESS_KEY_ID", data.accessKeyId);
    copyableRow(awsStsResult, "AWS_SECRET_ACCESS_KEY", data.secretAccessKey);
    copyableRow(awsStsResult, "AWS_SESSION_TOKEN", data.sessionToken);

    var exportBlock = document.createElement("pre");
    exportBlock.style.cssText = "font-size:0.75rem; overflow-x:auto; background:var(--bg); padding:0.5rem; border-radius:6px;";
    exportBlock.textContent =
      "export AWS_ACCESS_KEY_ID=" + data.accessKeyId + "\n" +
      "export AWS_SECRET_ACCESS_KEY=" + data.secretAccessKey + "\n" +
      "export AWS_SESSION_TOKEN=" + data.sessionToken;
    awsStsResult.appendChild(exportBlock);
  }

  function renderProfile(me) {
    profileAvatarEl.src = me.avatar || "";
    profileNameEl.textContent = me.name || me.username || "";
    profileUsernameEl.textContent = me.username || "—";
    profileEmailEl.textContent = me.email || "—";
    profileRoleEl.textContent = me.role || "—";

    profileOrgsEl.innerHTML = "";
    if (me.orgs && me.orgs.length) {
      profileOrgsRow.style.display = "";
      me.orgs.forEach(function (org) {
        var tag = document.createElement("span");
        tag.className = "citation";
        tag.textContent = org;
        profileOrgsEl.appendChild(tag);
      });
    } else {
      profileOrgsRow.style.display = "none";
    }
  }

  // Check session on load.
  fetch("/api/v1/me", { credentials: "include" })
    .then(function (res) {
      if (!res.ok) throw new Error("not logged in");
      return res.json();
    })
    .then(function (me) {
      loginView.style.display = "none";
      tabsEl.style.display = "flex";
      if (me.role === "admin") {
        adminTabBtn.style.display = "";
        homeAdminCard.style.display = "";
      }
      renderProfile(me);
      showView("home");
      statusEl.textContent = me.username ? "Logged in as " + me.username : "";
    })
    .catch(function () {
      loginView.style.display = "flex";
      tabsEl.style.display = "none";
      homeView.style.display = "none";
      chatView.style.display = "none";
      awsView.style.display = "none";
      adminView.style.display = "none";
    });
})();
