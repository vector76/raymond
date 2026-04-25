(function () {
  "use strict";

  // --- State ---
  var selectedRunID = null;
  var eventSource = null;
  var pollTimer = null;
  var POLL_INTERVAL = 3000;
  // Cached signature of the last resolved-inputs payload we rendered, scoped
  // to the run it was for. Resolved inputs are append-only and immutable per
  // the design, so polling can short-circuit DOM rebuilds when the response
  // hasn't changed — preventing image/PDF embeds from flashing every tick.
  var lastResolvedSig = null;

  // --- DOM refs ---
  var workflowsEl = document.getElementById("workflows");
  var activeRunsEl = document.getElementById("active-runs");
  var historyRunsEl = document.getElementById("history-runs");
  var pendingSection = document.getElementById("pending-inputs-section");
  var pendingEl = document.getElementById("pending-inputs");
  var outputSection = document.getElementById("output-section");
  var resolvedInputsEl = document.getElementById("resolved-inputs");
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

  // formatBytes returns a short, human-readable file size (e.g. "1.2 MB").
  function formatBytes(n) {
    if (!n || n <= 0) return "0 B";
    var units = ["B", "KB", "MB", "GB"];
    var i = 0;
    var v = n;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    if (i === 0) return v + " " + units[i];
    return v.toFixed(v < 10 ? 1 : 0) + " " + units[i];
  }

  // MIME types the file content endpoint will serve with
  // ?disposition=inline. Mirrors inlineAllowedContentTypes in http.go.
  var INLINE_IMAGE_TYPES = {
    "image/png": true,
    "image/jpeg": true,
    "image/gif": true,
    "image/webp": true,
  };

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
      // The counter, when present, is _1, _2, ... — never _0 (the
      // generator only adds it to disambiguate collisions).
      var m = run.run_id.match(/^workflow_(\d{4})-(\d{2})-(\d{2})_(\d{2})-(\d{2})-(\d{2})-\d+(?:_[1-9]\d*)?$/);
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

      var input = wf.input || { mode: "optional", label: "", description: "" };
      var mode = input.mode || "optional";
      var label = input.label || "Input";
      var placeholder = label + (mode === "required" ? " (required)" : " (optional)");
      var inputHTML =
        mode === "none"
          ? ""
          : '<div class="workflow-input">' +
              '<textarea rows="1" placeholder="' + escapeHTML(placeholder) + '"' +
              (mode === "required" ? ' required' : '') + '></textarea>' +
              (input.description
                ? '<div class="workflow-input-help">' + escapeHTML(input.description) + '</div>'
                : '') +
            '</div>';

      card.innerHTML =
        '<div class="workflow-card-header">' +
          '<span class="workflow-name">' + escapeHTML(wf.name || wf.id) + '</span>' +
          '<span class="workflow-id">' + escapeHTML(wf.id) + '</span>' +
        '</div>' +
        (wf.description
          ? '<div class="workflow-description">' + escapeHTML(wf.description) + '</div>'
          : '') +
        '<div class="workflow-actions">' +
          inputHTML +
          '<button class="btn btn-primary">Launch</button>' +
        '</div>';

      var textarea = card.querySelector("textarea");
      var btn = card.querySelector("button");

      btn.addEventListener("click", function () {
        var value = textarea ? textarea.value.trim() : "";
        if (mode === "required" && !value) {
          alert(label + " is required");
          return;
        }
        btn.disabled = true;
        btn.textContent = "Launching...";
        launchWorkflow(wf.id, value).then(function (resp) {
          if (textarea) textarea.value = "";
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

      if (textarea) {
        textarea.addEventListener("keydown", function (e) {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            btn.click();
          }
        });
      }

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
    var active = isActive(run.status);

    card.innerHTML =
      '<div class="run-card-header">' +
        '<span class="run-id" title="' + escapeHTML(run.run_id || "") + '">' + escapeHTML(label) + '</span>' +
        '<span class="badge badge-' + escapeHTML(run.status) + '">' + escapeHTML(run.status) + '</span>' +
        (active
          ? ''
          : '<button class="run-delete" type="button" title="Delete run" aria-label="Delete run">×</button>') +
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

    var delBtn = card.querySelector(".run-delete");
    if (delBtn) {
      delBtn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (!confirm("Delete run " + run.run_id + "?\n\nThis removes its state file and tasks directory from .raymond/.")) {
          return;
        }
        delBtn.disabled = true;
        deleteRun(run.run_id).then(function () {
          if (selectedRunID === run.run_id) {
            selectedRunID = null;
            outputSection.style.display = "none";
            if (eventSource) { eventSource.close(); eventSource = null; }
          }
          refreshAll();
        }).catch(function (err) {
          delBtn.disabled = false;
          alert("Delete failed: " + err.message);
        });
      });
    }

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

  // shouldPreserveCard reports whether an existing pending-input card holds
  // user state we shouldn't blow away on a poll re-render: a non-empty file
  // selection or typed-but-unsent text. Focus is handled separately in
  // renderPendingInputs by skipping the whole render — moving a focused node
  // via appendChild can drop the user's cursor in some browsers.
  function shouldPreserveCard(card) {
    if (!card) return false;
    var fileInputs = card.querySelectorAll('input[type="file"]');
    for (var i = 0; i < fileInputs.length; i++) {
      if (fileInputs[i].files && fileInputs[i].files.length > 0) return true;
    }
    var ta = card.querySelector("textarea");
    if (ta && ta.value !== "") return true;
    return false;
  }

  function renderPendingInputs(inputs) {
    if (!inputs || inputs.length === 0) {
      // Don't clear while focus is inside the section — a textarea blur in
      // the middle of typing is the same UX bug the focus-skip below avoids.
      // The next poll once focus moves will clean up.
      if (pendingEl.contains(document.activeElement)) return;
      pendingSection.style.display = "none";
      pendingEl.innerHTML = "";
      return;
    }
    pendingSection.style.display = "";

    // Skip re-render entirely while focus is inside the pending section so
    // we don't steal the user's cursor mid-edit. Per-card preservation
    // (below) keeps cards with selected files or typed text intact when
    // focus is elsewhere on the page.
    if (pendingEl.contains(document.activeElement)) {
      return;
    }

    // Index existing cards by input_id so in-progress cards survive polling.
    var existing = {};
    var current = pendingEl.querySelectorAll(".input-card");
    for (var i = 0; i < current.length; i++) {
      var iid = current[i].dataset.inputId;
      if (iid) existing[iid] = current[i];
    }

    var ordered = [];
    inputs.forEach(function (input) {
      var prior = existing[input.input_id];
      if (prior && shouldPreserveCard(prior)) {
        ordered.push(prior);
      } else {
        ordered.push(buildPendingInputCard(input));
      }
      delete existing[input.input_id];
    });

    pendingEl.innerHTML = "";
    ordered.forEach(function (c) { pendingEl.appendChild(c); });
  }

  // fileURL builds the file content endpoint URL for a staged or uploaded
  // file. The path segment is the recorded filename (already normalized);
  // the disposition query is set only for inline previews of allowlisted
  // MIME types — see inlineAllowedContentTypes in http.go.
  function fileURL(runID, inputID, name, inline) {
    var url = "/runs/" + encodeURIComponent(runID) +
      "/inputs/" + encodeURIComponent(inputID) +
      "/files/" + encodeURIComponent(name);
    if (inline) url += "?disposition=inline";
    return url;
  }

  function buildDisplayFileEl(runID, inputID, file) {
    var ct = (file.content_type || "").toLowerCase();
    var wrapper = document.createElement("div");
    wrapper.className = "input-display-file";

    if (INLINE_IMAGE_TYPES[ct]) {
      var img = document.createElement("img");
      img.className = "input-display-image";
      img.src = fileURL(runID, inputID, file.name, true);
      img.alt = file.name;
      wrapper.appendChild(img);
    } else if (ct === "application/pdf") {
      var emb = document.createElement("embed");
      emb.className = "input-display-pdf";
      emb.type = "application/pdf";
      emb.src = fileURL(runID, inputID, file.name, true);
      wrapper.appendChild(emb);
    } else {
      var link = document.createElement("a");
      link.href = fileURL(runID, inputID, file.name, false);
      link.textContent = "Download " + file.name;
      link.className = "input-display-link";
      wrapper.appendChild(link);
    }

    var meta = document.createElement("div");
    meta.className = "input-display-file-meta";
    meta.textContent = file.name + " (" + formatBytes(file.size) +
      (file.content_type ? ", " + file.content_type : "") + ")";
    wrapper.appendChild(meta);
    return wrapper;
  }

  function buildSlotControl(form, slot) {
    var row = document.createElement("div");
    row.className = "input-file-row";

    var label = document.createElement("label");
    label.className = "input-file-label";
    label.textContent = slot.name;
    row.appendChild(label);

    var input = document.createElement("input");
    input.type = "file";
    input.name = slot.name;
    if (slot.mime && slot.mime.length > 0 && slot.mime.length <= 5) {
      input.accept = slot.mime.join(",");
    }
    row.appendChild(input);

    if (slot.mime && slot.mime.length > 0) {
      var help = document.createElement("div");
      help.className = "input-file-help";
      help.textContent = "Allowed: " + slot.mime.join(", ");
      row.appendChild(help);
    }

    var err = document.createElement("div");
    err.className = "input-file-error";
    row.appendChild(err);

    form.appendChild(row);
    return { fieldName: slot.name, inputElement: input, errorEl: err };
  }

  function buildBucketControl(form, bucket) {
    var row = document.createElement("div");
    row.className = "input-file-row";

    var label = document.createElement("label");
    label.className = "input-file-label";
    label.textContent = "Files";
    row.appendChild(label);

    var input = document.createElement("input");
    input.type = "file";
    input.name = "files";
    input.multiple = true;
    if (bucket && bucket.mime && bucket.mime.length > 0 && bucket.mime.length <= 5) {
      input.accept = bucket.mime.join(",");
    }
    row.appendChild(input);

    if (bucket) {
      var bits = [];
      if (bucket.max_count) bits.push("up to " + bucket.max_count + " file" + (bucket.max_count === 1 ? "" : "s"));
      if (bucket.max_size_per_file) bits.push(formatBytes(bucket.max_size_per_file) + " per file");
      if (bucket.max_total_size) bits.push(formatBytes(bucket.max_total_size) + " total");
      if (bucket.mime && bucket.mime.length > 0) bits.push("MIME: " + bucket.mime.join(", "));
      if (bits.length > 0) {
        var help = document.createElement("div");
        help.className = "input-file-help";
        help.textContent = bits.join("; ");
        row.appendChild(help);
      }
    }

    var err = document.createElement("div");
    err.className = "input-file-error";
    row.appendChild(err);

    form.appendChild(row);
    return { fieldName: "files", inputElement: input, errorEl: err };
  }

  // applyUploadError surfaces a server-side validation failure in the form.
  // The constraint string identifies which rule fired (see uploadErrorResponse
  // in http.go) and we use it plus the message text to highlight the offending
  // control. Anything we can't pin to a control falls back to a form-level
  // banner.
  function applyUploadError(constraint, message, controls, formErrEl) {
    var slotMatch = /slot "([^"]+)"/.exec(message || "");
    var fileMatch = /(?:file|filename) "([^"]+)"/.exec(message || "");
    var matched = false;

    function highlight(ctrl) {
      ctrl.inputElement.classList.add("input-error");
      if (ctrl.errorEl) {
        ctrl.errorEl.textContent = message;
      }
    }

    if (constraint === "slot_missing" || constraint === "slot_extra") {
      if (slotMatch) {
        for (var i = 0; i < controls.length; i++) {
          if (controls[i].fieldName === slotMatch[1]) {
            highlight(controls[i]);
            matched = true;
            break;
          }
        }
      }
    } else if (constraint === "max_file_size" ||
               constraint === "mime_not_allowed" ||
               constraint === "filename" ||
               constraint === "duplicate_filename" ||
               constraint === "collision_with_staged") {
      if (fileMatch) {
        // For slot mode the file name equals the slot field name.
        for (var j = 0; j < controls.length; j++) {
          if (controls[j].fieldName === fileMatch[1]) {
            highlight(controls[j]);
            matched = true;
            break;
          }
        }
      }
      if (!matched) {
        controls.forEach(highlight);
        matched = controls.length > 0;
      }
    } else if (constraint === "max_count" || constraint === "max_total_size") {
      controls.forEach(highlight);
      matched = controls.length > 0;
    }

    if (!matched && formErrEl) {
      formErrEl.textContent = message;
      formErrEl.classList.add("input-form-error-visible");
    }
  }

  function clearFormErrors(controls, formErrEl) {
    controls.forEach(function (ctrl) {
      ctrl.inputElement.classList.remove("input-error");
      if (ctrl.errorEl) ctrl.errorEl.textContent = "";
    });
    if (formErrEl) {
      formErrEl.textContent = "";
      formErrEl.classList.remove("input-form-error-visible");
    }
  }

  function buildPendingInputCard(input) {
    var card = document.createElement("div");
    card.className = "input-card";
    card.dataset.inputId = input.input_id;

    var shortInput = input.input_id.length > 12
      ? input.input_id.substring(0, 12) + "..."
      : input.input_id;

    var header = document.createElement("div");
    header.className = "input-card-header";
    header.innerHTML =
      '<span>Run: ' + escapeHTML(input.run_id) + '</span>' +
      '<span>Input: ' + escapeHTML(shortInput) + '</span>';
    card.appendChild(header);

    var promptEl = document.createElement("div");
    promptEl.className = "input-prompt";
    promptEl.textContent = input.prompt;
    card.appendChild(promptEl);

    var aff = input.file_affordance || null;
    var mode = aff ? aff.mode : "text_only";

    // Display files render above any upload control regardless of mode.
    var displayFiles = (input.staged_files || []).filter(function (f) {
      return f.source === "display";
    });
    if (displayFiles.length > 0) {
      var displaySection = document.createElement("div");
      displaySection.className = "input-display-files";
      displayFiles.forEach(function (f) {
        displaySection.appendChild(buildDisplayFileEl(input.run_id, input.input_id, f));
      });
      card.appendChild(displaySection);
    }

    var formEl = document.createElement("div");
    formEl.className = "input-form-wrapper";
    card.appendChild(formEl);

    var fileControls = [];
    if (mode === "slot") {
      (aff.slots || []).forEach(function (slot) {
        fileControls.push(buildSlotControl(formEl, slot));
      });
    } else if (mode === "bucket") {
      fileControls.push(buildBucketControl(formEl, aff.bucket || {}));
    }

    var formErr = document.createElement("div");
    formErr.className = "input-form-error";
    formEl.appendChild(formErr);

    var textRow = document.createElement("div");
    textRow.className = "input-form";
    formEl.appendChild(textRow);

    var textarea = document.createElement("textarea");
    textarea.rows = 2;
    textarea.placeholder = "Type your response...";
    textRow.appendChild(textarea);

    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn btn-primary";
    btn.textContent = "Send";
    textRow.appendChild(btn);

    var hasUpload = mode === "slot" || mode === "bucket";

    btn.addEventListener("click", function () {
      clearFormErrors(fileControls, formErr);

      if (!hasUpload) {
        var trimmed = textarea.value.trim();
        if (!trimmed) return;
        btn.disabled = true;
        btn.textContent = "Sending...";
        submitInput(input.run_id, input.input_id, trimmed).then(function () {
          card.style.opacity = "0.5";
          refreshAll();
        }).catch(function (err) {
          btn.disabled = false;
          btn.textContent = "Send";
          formErr.textContent = "Failed to send: " + err.message;
          formErr.classList.add("input-form-error-visible");
        });
        return;
      }

      // Multipart path: build FormData with the text response and each file
      // part keyed by its slot name (slot mode) or the shared "files" name
      // (bucket mode). The server enforces all caps and constraints; client
      // pre-checks here only catch the obvious "no file selected" case so
      // the user gets a fast pointer to the offending control.
      var fd = new FormData();
      fd.append("response", textarea.value);

      if (mode === "slot") {
        for (var i = 0; i < fileControls.length; i++) {
          var ctrl = fileControls[i];
          var files = ctrl.inputElement.files;
          if (!files || files.length === 0) {
            ctrl.inputElement.classList.add("input-error");
            if (ctrl.errorEl) ctrl.errorEl.textContent = "Required";
            formErr.textContent = "Slot \"" + ctrl.fieldName + "\" is required";
            formErr.classList.add("input-form-error-visible");
            return;
          }
          fd.append(ctrl.fieldName, files[0]);
        }
      } else { // bucket
        var bucketCtrl = fileControls[0];
        var bfiles = bucketCtrl.inputElement.files;
        if (!bfiles || bfiles.length === 0) {
          bucketCtrl.inputElement.classList.add("input-error");
          formErr.textContent = "At least one file is required";
          formErr.classList.add("input-form-error-visible");
          return;
        }
        for (var j = 0; j < bfiles.length; j++) {
          fd.append(bucketCtrl.fieldName, bfiles[j]);
        }
      }

      btn.disabled = true;
      btn.textContent = "Sending...";
      submitInputMultipart(input.run_id, input.input_id, fd).then(function () {
        card.style.opacity = "0.5";
        refreshAll();
      }).catch(function (err) {
        btn.disabled = false;
        btn.textContent = "Send";
        applyUploadError(err.constraint || "", err.message || "submit failed",
          fileControls, formErr);
      });
    });

    textarea.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        btn.click();
      }
    });

    return card;
  }

  // buildResolvedInputCard renders a historical resolved input: the prompt,
  // any display files the user saw, the response text, then any uploaded
  // files. File rendering reuses buildDisplayFileEl so pending and historical
  // views look identical.
  function buildResolvedInputCard(runID, ri) {
    var card = document.createElement("div");
    card.className = "input-card resolved-input-card";
    card.dataset.inputId = ri.input_id;

    var shortInput = ri.input_id && ri.input_id.length > 12
      ? ri.input_id.substring(0, 12) + "..."
      : (ri.input_id || "");

    var header = document.createElement("div");
    header.className = "input-card-header";
    header.innerHTML =
      '<span>Resolved input: ' + escapeHTML(shortInput) + '</span>' +
      (ri.agent_id ? '<span>Agent: ' + escapeHTML(ri.agent_id) + '</span>' : '');
    card.appendChild(header);

    if (ri.prompt) {
      var promptEl = document.createElement("div");
      promptEl.className = "input-prompt";
      promptEl.textContent = ri.prompt;
      card.appendChild(promptEl);
    }

    var staged = ri.staged_files || [];
    if (staged.length > 0) {
      var stagedSection = document.createElement("div");
      stagedSection.className = "input-display-files";
      staged.forEach(function (f) {
        stagedSection.appendChild(buildDisplayFileEl(runID, ri.input_id, f));
      });
      card.appendChild(stagedSection);
    }

    if (ri.response_text) {
      var resp = document.createElement("div");
      resp.className = "resolved-response";
      var label = document.createElement("div");
      label.className = "resolved-response-label";
      label.textContent = "Response";
      resp.appendChild(label);
      var body = document.createElement("div");
      body.className = "resolved-response-body";
      body.textContent = ri.response_text;
      resp.appendChild(body);
      card.appendChild(resp);
    }

    var uploaded = ri.uploaded_files || [];
    if (uploaded.length > 0) {
      var upLabel = document.createElement("div");
      upLabel.className = "resolved-uploads-label";
      upLabel.textContent = "Uploaded files";
      card.appendChild(upLabel);
      var uploadSection = document.createElement("div");
      uploadSection.className = "input-display-files";
      uploaded.forEach(function (f) {
        uploadSection.appendChild(buildDisplayFileEl(runID, ri.input_id, f));
      });
      card.appendChild(uploadSection);
    }

    return card;
  }

  function renderResolvedInputs(runID, items) {
    resolvedInputsEl.innerHTML = "";
    if (!items || items.length === 0) {
      resolvedInputsEl.style.display = "none";
      return;
    }
    resolvedInputsEl.style.display = "";
    items.forEach(function (ri) {
      resolvedInputsEl.appendChild(buildResolvedInputCard(runID, ri));
    });
  }

  function refreshResolvedInputs(runID) {
    if (!runID) return Promise.resolve();
    return apiGet("/runs/" + encodeURIComponent(runID) + "/resolved-inputs").then(function (items) {
      // Guard against stale fetches racing a run switch.
      if (selectedRunID !== runID) return;
      var sig = runID + ":" + JSON.stringify(items || []);
      if (sig === lastResolvedSig) return;
      lastResolvedSig = sig;
      renderResolvedInputs(runID, items);
    }).catch(function () {
      // Leave previously-rendered cards in place on a transient fetch
      // failure — resolved inputs are immutable, so stale-but-correct beats
      // a flash of empty state. The next successful poll repopulates.
    });
  }

  // --- Actions ---
  function selectRun(runID, status) {
    selectedRunID = runID;
    outputSection.style.display = "";
    outputRunID.textContent = runID;
    outputLog.textContent = "";
    resolvedInputsEl.innerHTML = "";
    resolvedInputsEl.style.display = "none";
    // Reset the dedup signature so the new run's history renders fresh.
    lastResolvedSig = null;

    // Show cancel button for active runs
    if (isActive(status)) {
      cancelBtn.style.display = "";
    } else {
      cancelBtn.style.display = "none";
    }

    // Re-render cards to update selection
    refreshRuns();

    // Fetch resolved-input history so the panel shows past prompts, files,
    // and responses alongside the live log.
    refreshResolvedInputs(runID);

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
      // EventSource fires onerror on any close, including a normal
      // server-initiated end-of-stream after the replay finishes for a
      // completed/recovered run. Only show the [closed] line for an
      // unexpected disconnect (readyState CONNECTING == 0, the browser
      // is trying to reconnect). When the server cleanly closed the
      // stream readyState is CLOSED (2) and we suppress the line.
      if (eventSource && eventSource.readyState !== EventSource.CLOSED) {
        appendOutputLine("[SSE connection closed]");
      }
      if (eventSource) {
        eventSource.close();
        eventSource = null;
      }
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
        if (evt.ResultPayload) {
          // The result payload is the reason the agent stopped — either the
          // workflow author's chosen result string, or a system-synthesized
          // message (e.g. "Workflow terminated: budget exceeded ..." from
          // the markdown executor's budget guard). Surface it so a run that
          // ends unexpectedly doesn't read as just "terminated".
          line += " — " + evt.ResultPayload;
        }
        break;
      case "transition_occurred":
        // Suppress self-transitions: a paused agent leaves CurrentState
        // unchanged, and the orchestrator still emits TransitionOccurred
        // with FromState == ToState. The accompanying [paused] line
        // already conveys the outcome.
        if (evt.FromState && evt.FromState === evt.ToState) {
          return;
        }
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

  // submitInputMultipart posts an upload submission to the deliver endpoint.
  // The browser sets the Content-Type header (with boundary) automatically
  // when body is a FormData. On a 4xx/5xx the server returns a JSON body
  // shaped like {error, constraint}; we attach `constraint` to the thrown
  // Error so the form can highlight the right control.
  function submitInputMultipart(runID, inputID, formData) {
    return fetch(
      "/runs/" + encodeURIComponent(runID) + "/inputs/" + encodeURIComponent(inputID),
      { method: "POST", body: formData }
    ).then(function (res) {
      if (!res.ok) {
        return res.json().then(function (body) {
          var err = new Error(body && body.error ? body.error : ("HTTP " + res.status));
          if (body && body.constraint) err.constraint = body.constraint;
          err.status = res.status;
          throw err;
        }, function () {
          throw new Error("HTTP " + res.status);
        });
      }
      return res.json();
    });
  }

  function cancelRun(runID) {
    return apiPost("/runs/" + encodeURIComponent(runID) + "/cancel", {});
  }

  function deleteRun(runID) {
    return fetch("/runs/" + encodeURIComponent(runID), { method: "DELETE" })
      .then(function (res) {
        if (!res.ok) {
          return res.json().then(function (body) {
            throw new Error(body.error || ("HTTP " + res.status));
          }, function () {
            throw new Error("HTTP " + res.status);
          });
        }
      });
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
      var pendingP = refreshPendingInputs(runs);
      // Re-fetch resolved inputs for the selected run so the history view
      // updates as new awaits resolve while the panel is open.
      var resolvedP = selectedRunID ? refreshResolvedInputs(selectedRunID) : Promise.resolve();
      return Promise.all([pendingP, resolvedP]);
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
