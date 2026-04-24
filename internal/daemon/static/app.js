(function () {
  "use strict";

  // --- State ---
  var selectedRunID = null;
  var eventSource = null;
  var pollTimer = null;
  var POLL_INTERVAL = 3000;

  // --- DOM refs ---
  var workflowsEl = document.getElementById("workflows");
  var activeRunsEl = document.getElementById("active-runs");
  var historyRunsEl = document.getElementById("history-runs");
  var pendingSection = document.getElementById("pending-inputs-section");
  var pendingEl = document.getElementById("pending-inputs");
  var outputSection = document.getElementById("output-section");
  var outputLog = document.getElementById("output-log");
  var outputRunID = document.getElementById("output-run-id");
  var cancelBtn = document.getElementById("cancel-btn");
  var connStatus = document.getElementById("conn-status");
  var themeToggle = document.getElementById("theme-toggle");

  // --- Helpers ---
  function formatElapsed(secs) {
    var total = Math.round(secs);
    if (total < 60) return total + "s";
    var m = Math.floor(total / 60);
    var s = total % 60;
    return m + "m " + s + "s";
  }

  function formatCost(usd) {
    if (usd < 0.01) return "&lt;$0.01";
    return "$" + usd.toFixed(2);
  }

  function escapeHTML(str) {
    var div = document.createElement("div");
    div.textContent = str;
    return div.innerHTML;
  }

  function isActive(status) {
    return status === "running" || status === "awaiting_input";
  }

  // pad2 returns a 2-digit zero-padded string for h/m/s formatting.
  function pad2(n) {
    return n < 10 ? "0" + n : "" + n;
  }

  // formatLaunchTime returns a short, human-readable label for a run's launch
  // moment. Prefers the server-supplied started_at (RFC3339) and falls back to
  // parsing the timestamp embedded in the run_id ("workflow_YYYY-MM-DD_HH-MM-SS-...").
  // Same-day launches show "HH:MM:SS"; otherwise "Mmm DD HH:MM:SS".
  function formatLaunchTime(run) {
    var d = null;
    if (run.started_at) {
      var t = new Date(run.started_at);
      if (!isNaN(t.getTime()) && t.getFullYear() > 1) {
        d = t;
      }
    }
    if (!d && run.run_id) {
      // run_id format: workflow_YYYY-MM-DD_HH-MM-SS-MICROS[_N].
      // Require at least one digit of micros so partial / malformed ids
      // (e.g. "workflow_2026-04-23_18-37-29-garbage") don't match.
      var m = run.run_id.match(/^workflow_(\d{4})-(\d{2})-(\d{2})_(\d{2})-(\d{2})-(\d{2})-\d+(?:_\d+)?$/);
      if (m) {
        d = new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
      }
    }
    if (!d) {
      // Fall back to a truncated id when no timestamp is recoverable.
      return run.run_id && run.run_id.length > 12
        ? run.run_id.substring(0, 12) + "..."
        : (run.run_id || "?");
    }
    var hms = pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds());
    var now = new Date();
    var sameDay = d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() &&
      d.getDate() === now.getDate();
    if (sameDay) return hms;
    var months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun",
      "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    return months[d.getMonth()] + " " + pad2(d.getDate()) + " " + hms;
  }

  // --- API calls ---
  function apiGet(path) {
    return fetch(path).then(function (res) {
      if (!res.ok) throw new Error("HTTP " + res.status);
      return res.json();
    });
  }

  function apiPost(path, body) {
    return fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (res) {
      if (!res.ok) throw new Error("HTTP " + res.status);
      return res.json();
    });
  }

  // --- Rendering ---
  function renderWorkflows(workflows) {
    // Skip re-render while user is typing in a launch form.
    if (workflowsEl.contains(document.activeElement)) {
      return;
    }
    workflowsEl.innerHTML = "";
    if (!workflows || workflows.length === 0) {
      workflowsEl.innerHTML = '<div class="empty-state">No workflows discovered</div>';
      return;
    }

    workflows.forEach(function (wf) {
      var card = document.createElement("div");
      card.className = "workflow-card";

      card.innerHTML =
        '<div class="workflow-card-header">' +
          '<span class="workflow-name">' + escapeHTML(wf.name || wf.id) + '</span>' +
          '<span class="workflow-id">' + escapeHTML(wf.id) + '</span>' +
        '</div>' +
        (wf.description
          ? '<div class="workflow-description">' + escapeHTML(wf.description) + '</div>'
          : '') +
        '<div class="workflow-actions">' +
          '<textarea rows="1" placeholder="Input (optional)..."></textarea>' +
          '<button class="btn btn-primary">Launch</button>' +
        '</div>';

      var textarea = card.querySelector("textarea");
      var btn = card.querySelector("button");

      btn.addEventListener("click", function () {
        btn.disabled = true;
        btn.textContent = "Launching...";
        launchWorkflow(wf.id, textarea.value.trim()).then(function (resp) {
          textarea.value = "";
          btn.disabled = false;
          btn.textContent = "Launch";
          if (resp && resp.run_id) {
            // selectRun refreshes runs; the next poll tick handles conn-status
            // and pending inputs, so no separate refreshAll is needed.
            selectRun(resp.run_id, resp.status || "running");
          } else {
            refreshAll();
          }
        }).catch(function (err) {
          btn.disabled = false;
          btn.textContent = "Launch";
          alert("Launch failed: " + err.message);
        });
      });

      textarea.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          btn.click();
        }
      });

      workflowsEl.appendChild(card);
    });
  }

  function renderRunCard(run, container) {
    var card = document.createElement("div");
    card.className = "run-card" + (run.run_id === selectedRunID ? " selected" : "");
    card.dataset.runId = run.run_id;

    // Show launch time as the primary label — run_ids share the "workflow_YYYY"
    // prefix so a truncated id is not a useful differentiator. Full id is in
    // the title attribute for hover.
    var label = formatLaunchTime(run);

    card.innerHTML =
      '<div class="run-card-header">' +
        '<span class="run-id" title="' + escapeHTML(run.run_id || "") + '">' + escapeHTML(label) + '</span>' +
        '<span class="badge badge-' + escapeHTML(run.status) + '">' + escapeHTML(run.status) + '</span>' +
      '</div>' +
      '<div class="run-workflow">' + escapeHTML(run.workflow_id || "unknown") + '</div>' +
      '<div class="run-meta">' +
        '<span>' + formatCost(run.cost_usd) + '</span>' +
        '<span>' + formatElapsed(run.elapsed_seconds) + '</span>' +
        (run.agents && run.agents.length > 0
          ? '<span>' + run.agents.length + ' agent' + (run.agents.length !== 1 ? 's' : '') + '</span>'
          : '') +
      '</div>';

    card.addEventListener("click", function () {
      selectRun(run.run_id, run.status);
    });

    container.appendChild(card);
  }

  // sortRunsNewestFirst sorts in place by started_at (descending). Falls back
  // to run_id (descending) when timestamps tie or are missing — this is the
  // stable case for recovered runs that share a zero started_at and would
  // otherwise reorder on every poll due to map iteration on the server.
  function sortRunsNewestFirst(runs) {
    runs.sort(function (a, b) {
      var aT = a.started_at || "";
      var bT = b.started_at || "";
      if (bT > aT) return 1;
      if (bT < aT) return -1;
      var aID = a.run_id || "";
      var bID = b.run_id || "";
      if (bID > aID) return 1;
      if (bID < aID) return -1;
      return 0;
    });
  }

  function renderActiveRuns(runs) {
    activeRunsEl.innerHTML = "";
    var active = runs.filter(function (r) { return isActive(r.status); });
    if (active.length === 0) {
      activeRunsEl.innerHTML = '<div class="empty-state">No active runs</div>';
      return;
    }
    sortRunsNewestFirst(active);
    active.forEach(function (r) { renderRunCard(r, activeRunsEl); });
  }

  function renderHistory(runs) {
    historyRunsEl.innerHTML = "";
    var done = runs.filter(function (r) { return !isActive(r.status); });
    if (done.length === 0) {
      historyRunsEl.innerHTML = '<div class="empty-state">No completed runs</div>';
      return;
    }
    sortRunsNewestFirst(done);
    done.forEach(function (r) { renderRunCard(r, historyRunsEl); });
  }

  function renderPendingInputs(inputs) {
    if (!inputs || inputs.length === 0) {
      pendingSection.style.display = "none";
      return;
    }
    // Skip re-render while user is typing in a pending input form;
    // next poll after they finish will refresh.
    if (pendingEl.contains(document.activeElement)) {
      return;
    }
    pendingSection.style.display = "";
    pendingEl.innerHTML = "";

    inputs.forEach(function (input) {
      var card = document.createElement("div");
      card.className = "input-card";

      var shortInput = input.input_id.length > 12
        ? input.input_id.substring(0, 12) + "..."
        : input.input_id;

      card.innerHTML =
        '<div class="input-card-header">' +
          '<span>Run: ' + escapeHTML(input.run_id) + '</span>' +
          '<span>Input: ' + escapeHTML(shortInput) + '</span>' +
        '</div>' +
        '<div class="input-prompt">' + escapeHTML(input.prompt) + '</div>' +
        '<div class="input-form">' +
          '<textarea rows="2" placeholder="Type your response..."></textarea>' +
          '<button class="btn btn-primary">Send</button>' +
        '</div>';

      var textarea = card.querySelector("textarea");
      var btn = card.querySelector("button");

      btn.addEventListener("click", function () {
        var response = textarea.value.trim();
        if (!response) return;
        btn.disabled = true;
        btn.textContent = "Sending...";
        submitInput(input.run_id, input.input_id, response).then(function () {
          card.style.opacity = "0.5";
          refreshAll();
        }).catch(function (err) {
          btn.disabled = false;
          btn.textContent = "Send";
          alert("Failed to send: " + err.message);
        });
      });

      // Allow Ctrl+Enter / Cmd+Enter to submit
      textarea.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          btn.click();
        }
      });

      pendingEl.appendChild(card);
    });
  }

  // --- Actions ---
  function selectRun(runID, status) {
    selectedRunID = runID;
    outputSection.style.display = "";
    outputRunID.textContent = runID;
    outputLog.textContent = "";

    // Show cancel button for active runs
    if (isActive(status)) {
      cancelBtn.style.display = "";
    } else {
      cancelBtn.style.display = "none";
    }

    // Re-render cards to update selection
    refreshRuns();

    // Connect SSE
    connectSSE(runID);
  }

  function connectSSE(runID) {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }

    eventSource = new EventSource("/runs/" + encodeURIComponent(runID) + "/output");

    eventSource.onmessage = function (e) {
      try {
        var evt = JSON.parse(e.data);
        appendOutputEvent(evt);
      } catch (err) {
        appendOutputLine(e.data);
      }
    };

    eventSource.onerror = function () {
      appendOutputLine("[SSE connection closed]");
      eventSource.close();
      eventSource = null;
    };
  }

  function appendOutputEvent(evt) {
    var line = "";
    switch (evt.type) {
      case "workflow_started":
        line = "=== Workflow: " + (evt.WorkflowID || "?") + " ===";
        break;
      case "workflow_completed":
        line = "=== Workflow completed — cost: $" + (evt.TotalCostUSD || 0).toFixed(4) + " ===";
        break;
      case "state_started":
        line = "--- State: " + (evt.StateName || "?") + " [" + (evt.StateType || "") + "] ---";
        break;
      case "state_completed":
        line = "--- State completed: " + (evt.StateName || "?") + " ($" + (evt.CostUSD || 0).toFixed(4) + ") ---";
        break;
      case "tool_invocation":
        line = "[tool] " + (evt.ToolName || "?") + (evt.Detail ? " — " + evt.Detail : "");
        break;
      case "progress_message":
        line = evt.Message || "";
        break;
      case "script_output":
        if (evt.Stdout) line += evt.Stdout;
        if (evt.Stderr) line += (line ? "\n" : "") + evt.Stderr;
        if (!line) line = "[script] exit " + (evt.ExitCode || 0);
        break;
      case "error_occurred":
        line = "[error] " + (evt.ErrorMessage || "unknown");
        break;
      case "agent_await_started":
        line = "[awaiting input] " + (evt.Prompt || "");
        break;
      case "agent_paused":
        line = "[paused] " + (evt.Reason || "");
        if (evt.Error) {
          line += ": " + evt.Error;
        }
        break;
      case "agent_spawned":
        line = "[spawned] " + (evt.NewAgentID || "?") + " → " + (evt.InitialState || "?");
        break;
      case "agent_terminated":
        line = "[terminated] agent " + (evt.AgentID || "?");
        break;
      case "transition_occurred":
        line = "[transition] " + (evt.FromState || "?") + " → " + (evt.ToState || "(end)");
        break;
      case "claude_stream_output":
      case "claude_invocation_started":
        return; // skip noisy stream-level events
      default:
        line = "[" + (evt.type || "event") + "] " + JSON.stringify(evt);
    }
    appendOutputLine(line);
  }

  function appendOutputLine(text) {
    var shouldScroll = outputLog.scrollTop + outputLog.clientHeight >= outputLog.scrollHeight - 20;
    outputLog.appendChild(document.createTextNode(text + "\n"));
    if (shouldScroll) {
      outputLog.scrollTop = outputLog.scrollHeight;
    }
  }

  function submitInput(runID, inputID, response) {
    return apiPost(
      "/runs/" + encodeURIComponent(runID) + "/inputs/" + encodeURIComponent(inputID),
      { response: response }
    );
  }

  function cancelRun(runID) {
    return apiPost("/runs/" + encodeURIComponent(runID) + "/cancel", {});
  }

  function launchWorkflow(workflowID, input) {
    return apiPost("/runs", { workflow_id: workflowID, input: input });
  }

  // --- Polling ---
  function refreshWorkflows() {
    return apiGet("/workflows").then(function (workflows) {
      renderWorkflows(workflows);
    }).catch(function (err) {
      // Workflows are only fetched at startup, so surface the failure
      // instead of leaving the section silently empty.
      if (workflowsEl.contains(document.activeElement)) return;
      workflowsEl.innerHTML =
        '<div class="empty-state">Failed to load workflows: ' +
        escapeHTML(err.message) +
        ' &mdash; <a href="#" id="workflows-retry">Retry</a></div>';
      var retry = document.getElementById("workflows-retry");
      if (retry) {
        retry.addEventListener("click", function (e) {
          e.preventDefault();
          workflowsEl.innerHTML = '<div class="empty-state">Loading...</div>';
          refreshWorkflows();
        });
      }
    });
  }

  function refreshRuns() {
    return apiGet("/runs").then(function (runs) {
      renderActiveRuns(runs);
      renderHistory(runs);
      return runs;
    });
  }

  function refreshPendingInputs(runs) {
    var awaiting = runs.filter(function (r) { return r.status === "awaiting_input"; });
    if (awaiting.length === 0) {
      renderPendingInputs([]);
      return Promise.resolve();
    }

    var promises = awaiting.map(function (r) {
      return apiGet("/runs/" + encodeURIComponent(r.run_id) + "/pending-inputs").catch(function () {
        return [];
      });
    });

    return Promise.all(promises).then(function (results) {
      var all = [];
      results.forEach(function (inputs) {
        if (Array.isArray(inputs)) {
          all = all.concat(inputs);
        }
      });
      renderPendingInputs(all);
    });
  }

  function refreshAll() {
    return refreshRuns().then(function (runs) {
      connStatus.textContent = runs.length + " run" + (runs.length !== 1 ? "s" : "");
      return refreshPendingInputs(runs);
    }).catch(function (err) {
      connStatus.textContent = "Error: " + err.message;
    });
  }

  function startPolling() {
    // Workflows are discovered once at registry startup and don't change
    // while the daemon runs, so fetch once rather than on every poll tick.
    refreshWorkflows();
    refreshAll();
    pollTimer = setInterval(refreshAll, POLL_INTERVAL);
  }

  // --- Theme ---
  // Outline icons (stroke="currentColor" so they inherit button color).
  var SUN_ICON =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"' +
    ' stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<circle cx="12" cy="12" r="4"/>' +
    '<path d="M12 2v2"/><path d="M12 20v2"/>' +
    '<path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/>' +
    '<path d="M2 12h2"/><path d="M20 12h2"/>' +
    '<path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>' +
    '</svg>';
  var MOON_ICON =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"' +
    ' stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>' +
    '</svg>';

  function currentTheme() {
    return document.documentElement.getAttribute("data-theme") === "dark" ? "dark" : "light";
  }

  function updateThemeToggle() {
    var isDark = currentTheme() === "dark";
    // Show the icon for the mode you would switch TO.
    themeToggle.innerHTML = isDark ? SUN_ICON : MOON_ICON;
    var label = "Switch to " + (isDark ? "light" : "dark") + " mode";
    themeToggle.setAttribute("aria-label", label);
    themeToggle.setAttribute("aria-pressed", isDark ? "true" : "false");
    themeToggle.title = label;
  }

  themeToggle.addEventListener("click", function () {
    var next = currentTheme() === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    try { localStorage.setItem("raymond-theme", next); } catch (e) {}
    updateThemeToggle();
  });

  updateThemeToggle();

  // --- Event listeners ---
  cancelBtn.addEventListener("click", function () {
    if (!selectedRunID) return;
    if (!confirm("Cancel run " + selectedRunID + "?")) return;
    cancelBtn.disabled = true;
    cancelRun(selectedRunID).then(function () {
      cancelBtn.style.display = "none";
      refreshAll();
    }).catch(function (err) {
      alert("Cancel failed: " + err.message);
    }).finally(function () {
      cancelBtn.disabled = false;
    });
  });

  // --- Init ---
  startPolling();
})();
