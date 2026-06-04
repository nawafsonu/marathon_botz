const checkpointForm = document.querySelector("#checkpoint-form");
const registrationForm = document.querySelector("#registration-form");
const eventSettingsForm = document.querySelector("#event-settings-form");
const importForm = document.querySelector("#import-form");
const checkpointManagerForm = document.querySelector("#checkpoint-manager-form");

const checkpointStatus = document.querySelector("#checkpoint-status");
const registrationStatus = document.querySelector("#registration-status");
const eventSettingsStatus = document.querySelector("#event-settings-status");
const importStatus = document.querySelector("#import-status");
const checkpointManagerStatus = document.querySelector("#checkpoint-manager-status");

function setStatus(node, message, kind = "") {
  if (!node) return;
  node.textContent = message;
  node.className = `form-status ${kind}`.trim();
}

async function postJSON(url, payload) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  const body = await response.json();
  if (!response.ok) {
    throw new Error(body.error || "Request failed.");
  }
  return body;
}

checkpointForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(checkpointForm);
  const payload = {
    checkpointId: form.get("checkpointId"),
    bibNumber: String(form.get("bibNumber") || "").toUpperCase(),
    volunteerId: form.get("volunteerId"),
  };
  setStatus(checkpointStatus, "Recording checkpoint...");
  try {
    const log = await postJSON("/api/checkpoint-logs", payload);
    setStatus(checkpointStatus, `${log.participant.bibNumber} recorded at ${log.checkpoint.name}.`, "success");
    checkpointForm.elements.namedItem("bibNumber").value = "";
    checkpointForm.elements.namedItem("bibNumber").focus();
    await refreshState();
  } catch (error) {
    setStatus(checkpointStatus, error.message, "error");
  }
});

registrationForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(registrationForm);
  setStatus(registrationStatus, "Registering runner...");
  try {
    const participant = await postJSON("/api/participants", {
      name: form.get("name"),
      phoneNumber: form.get("phoneNumber"),
      notes: form.get("notes"),
    });
    setStatus(registrationStatus, `${participant.name} registered as ${participant.bibNumber}.`, "success");
    registrationForm.reset();
    await refreshState();
  } catch (error) {
    setStatus(registrationStatus, error.message, "error");
  }
});

eventSettingsForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(eventSettingsForm);
  const localStart = form.get("startTime");
  setStatus(eventSettingsStatus, "Saving race setup...");
  try {
    const eventData = await postJSON("/api/event-settings", {
      distanceKm: Number(form.get("distanceKm")),
      startTime: new Date(localStart).toISOString(),
    });
    setStatus(eventSettingsStatus, `${eventData.distanceKm} KM start saved.`, "success");
    await refreshState();
  } catch (error) {
    setStatus(eventSettingsStatus, error.message, "error");
  }
});

checkpointManagerForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(checkpointManagerForm);
  setStatus(checkpointManagerStatus, "Adding checkpoint...");
  try {
    const checkpoint = await postJSON("/api/checkpoints", {
      name: form.get("name"),
      sequence: Number(form.get("sequence")),
      distanceKm: Number(form.get("distanceKm") || 0),
    });
    setStatus(checkpointManagerStatus, `${checkpoint.name} added to the race.`, "success");
    checkpointManagerForm.reset();
    await refreshState();
  } catch (error) {
    setStatus(checkpointManagerStatus, error.message, "error");
  }
});

importForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(importForm);
  setStatus(importStatus, "Importing runners...");
  try {
    const response = await fetch("/api/import-runners", {
      method: "POST",
      body: form,
    });
    const body = await response.json();
    if (!response.ok) {
      throw new Error(body.error || "Import failed.");
    }
    const skipped = body.errors?.length ? ` ${body.errors.length} rows skipped.` : "";
    setStatus(importStatus, `${body.created} runners imported.${skipped}`, body.errors?.length ? "error" : "success");
    importForm.reset();
    await refreshState();
  } catch (error) {
    setStatus(importStatus, error.message, "error");
  }
});

async function refreshState() {
  if (!document.querySelector("[data-page='dashboard']")) return;
  const response = await fetch("/api/state");
  if (!response.ok) return;
  const state = await response.json();
  updateEvent(state.event);
  updateStats(state.summary);
  updateCheckpoints(state.checkpoints);
  updateFeed(state.liveFeed);
  updateLeaderboard(state.leaderboard);
}

function updateEvent(eventData) {
  const distanceNode = document.querySelector("[data-event-distance]");
  if (distanceNode) {
    distanceNode.dataset.eventDistance = eventData.distanceKm;
    distanceNode.textContent = eventData.distanceKm;
  }
  const startNode = document.querySelector("[data-event-start]");
  if (startNode) {
    startNode.dataset.eventStart = eventData.startTime;
    startNode.textContent = new Date(eventData.startTime).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
  }
}

function updateStats(summary) {
  const pairs = {
    total: summary.totalParticipants,
    finished: summary.finished,
    active: summary.active,
    dnf: summary.dnf,
    completion: summary.completionRate,
  };
  for (const [key, value] of Object.entries(pairs)) {
    const node = document.querySelector(`[data-stat='${key}']`);
    if (node) node.textContent = value;
  }
}

function updateCheckpoints(checkpoints) {
  const list = document.querySelector("#checkpoint-list");
  if (list) {
    list.innerHTML = checkpoints.map((checkpoint) => `
      <li>
        <strong>${escapeHTML(checkpoint.name)}</strong>
        <span>${checkpoint.sequence} · ${Number(checkpoint.distanceKm).toFixed(1)} KM</span>
      </li>
    `).join("");
  }
  const select = checkpointForm?.elements.namedItem("checkpointId");
  if (select) {
    const selected = select.value;
    select.innerHTML = checkpoints.map((checkpoint) => `
      <option value="${escapeHTML(checkpoint.id)}">${escapeHTML(checkpoint.name)} · ${Number(checkpoint.distanceKm).toFixed(0)} KM</option>
    `).join("");
    if (selected && [...select.options].some((option) => option.value === selected)) {
      select.value = selected;
    }
  }
}

function updateFeed(feed) {
  const list = document.querySelector("#live-feed");
  if (!list) return;
  if (!feed.length) {
    list.innerHTML = `<li class="empty-state">No runners have reached this checkpoint yet.</li>`;
    return;
  }
  list.innerHTML = feed.map((log) => `
    <li>
      <strong>${escapeHTML(log.participant.bibNumber)}</strong>
      <span>${escapeHTML(log.checkpoint.name)}</span>
      <time>${new Date(log.timestamp).toLocaleTimeString([], { hour12: false })}</time>
    </li>
  `).join("");
}

function updateLeaderboard(entries) {
  const body = document.querySelector("#leaderboard-body");
  if (!body) return;
  if (!entries.length) {
    body.innerHTML = `<tr><td colspan="7" class="empty-state">No runners are registered yet.</td></tr>`;
    return;
  }
  body.innerHTML = entries.map((entry) => `
    <tr>
      <td class="rank">#${entry.rank}</td>
      <td>${escapeHTML(entry.bibNumber)}</td>
      <td><a href="/runners/${encodeURIComponent(entry.bibNumber)}">${escapeHTML(entry.runnerName)}</a></td>
      <td>${escapeHTML(entry.status)}</td>
      <td>${escapeHTML(entry.latestCheckpoint)}</td>
      <td>${escapeHTML(entry.finishTime)}</td>
      <td>${escapeHTML(entry.gap)}</td>
    </tr>
  `).join("");
}

function hydrateEventSettings() {
  if (!eventSettingsForm) return;
  const distanceSelect = eventSettingsForm.elements.namedItem("distanceKm");
  const distance = document.querySelector("[data-event-distance]")?.dataset.eventDistance;
  if (distanceSelect && distance) distanceSelect.value = distance;

  const startInput = eventSettingsForm.elements.namedItem("startTime");
  const start = document.querySelector("[data-event-start]")?.dataset.eventStart;
  if (startInput && start) startInput.value = toDateTimeLocalValue(start);
}

function toDateTimeLocalValue(iso) {
  const date = new Date(iso);
  const offset = date.getTimezoneOffset();
  const local = new Date(date.getTime() - offset * 60000);
  return local.toISOString().slice(0, 16);
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#039;",
  }[char]));
}

hydrateEventSettings();
setInterval(refreshState, 5000);
