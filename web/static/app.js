const checkpointForm = document.querySelector("#checkpoint-form");
const registrationForm = document.querySelector("#registration-form");
const eventSettingsForm = document.querySelector("#event-settings-form");
const eventCreateForm = document.querySelector("#event-create-form");
const importForm = document.querySelector("#import-form");
const checkpointManagerForm = document.querySelector("#checkpoint-manager-form");
const startRaceForm = document.querySelector("#start-race-form");
const volunteerForm = document.querySelector("#volunteer-form");
const navigationSelects = document.querySelectorAll("[data-navigation-select]");
const basePath = document.querySelector(".app-shell")?.dataset.basePath || "";

const checkpointStatus = document.querySelector("#checkpoint-status");
const registrationStatus = document.querySelector("#registration-status");
const eventSettingsStatus = document.querySelector("#event-settings-status");
const eventCreateStatus = document.querySelector("#event-create-status");
const importStatus = document.querySelector("#import-status");
const checkpointManagerStatus = document.querySelector("#checkpoint-manager-status");
const startRaceStatus = document.querySelector("#start-race-status");
const volunteerStatus = document.querySelector("#volunteer-status");
const analysisButtons = document.querySelectorAll("[data-analysis-endpoint]");
const chestReaderRoot = document.querySelector(".chest-reader");
const chestReaderStart = document.querySelector("#chest-reader-start");
const chestReaderStop = document.querySelector("#chest-reader-stop");
const chestReaderVideo = document.querySelector("#chest-reader-video");
const chestReaderCanvas = document.querySelector("#chest-reader-canvas");
const chestReaderPreview = document.querySelector(".chest-reader-preview");
const chestReaderCandidates = document.querySelector("#chest-reader-candidates");
const chestReaderConfigForm = document.querySelector("#chest-reader-config-form");
const chestReaderConfigSubmit = document.querySelector("#chest-reader-config-submit");
const chestReaderHelp = document.querySelector("#chest-reader-help");

let chestReaderStream = null;
let chestReaderTimer = null;
let chestReaderBusy = false;
let chestReaderLastBib = "";
let chestReaderStableCount = 0;

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
    participantId: form.get("participantId"),
    volunteerId: form.get("volunteerId"),
  };
  const bibNumber = String(form.get("bibNumber") || "").trim().toUpperCase();
  if (bibNumber) {
    payload.bibNumber = bibNumber;
  }
  setStatus(checkpointStatus, "Recording checkpoint...");
  try {
    const log = await postJSON(`${basePath}/api/checkpoint-logs`, payload);
    setStatus(checkpointStatus, `${log.participant.bibNumber} recorded at ${log.checkpoint.name}.`, "success");
    checkpointForm.elements.namedItem("bibNumber").value = "";
    checkpointForm.elements.namedItem("participantId").focus();
    await refreshState();
  } catch (error) {
    setStatus(checkpointStatus, error.message, "error");
  }
});

chestReaderStart?.addEventListener("click", async () => {
  if (!checkpointForm || !chestReaderRoot || chestReaderRoot.dataset.chestReaderEnabled !== "true") return;
  if (!navigator.mediaDevices?.getUserMedia) {
    setStatus(checkpointStatus, "Camera access requires HTTPS or localhost.", "error");
    return;
  }
  try {
    chestReaderStream = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: "environment", width: { ideal: 1280 }, height: { ideal: 720 } },
      audio: false,
    });
    chestReaderVideo.srcObject = chestReaderStream;
    await chestReaderVideo.play();
    chestReaderPreview.hidden = false;
    chestReaderStart.hidden = true;
    chestReaderStop.hidden = false;
    chestReaderLastBib = "";
    chestReaderStableCount = 0;
    setStatus(checkpointStatus, "Scanning chest number...");
    scanChestFrame();
    chestReaderTimer = window.setInterval(scanChestFrame, 900);
  } catch (error) {
    setStatus(checkpointStatus, `Camera unavailable: ${error.message}`, "error");
  }
});

chestReaderConfigSubmit?.addEventListener("click", async () => {
  if (!chestReaderConfigForm || !chestReaderRoot) return;
  const port = chestReaderConfigForm.querySelector("[name='port']")?.value || "";
  const url = chestReaderConfigForm.querySelector("[name='url']")?.value || "";
  setStatus(checkpointStatus, "Connecting OCR service...");
  chestReaderConfigSubmit.disabled = true;
  try {
    const result = await postJSON(`${basePath}/api/chest-reader/config`, {
      port,
      url,
    });
    chestReaderRoot.dataset.chestReaderEnabled = "true";
    if (chestReaderStart) chestReaderStart.disabled = false;
    if (chestReaderHelp) chestReaderHelp.textContent = `Connected to ${result.url}. Auto-submit after two matching reads.`;
    setStatus(checkpointStatus, result.message || "Chest reader connected.", "success");
  } catch (error) {
    setStatus(checkpointStatus, error.message, "error");
  } finally {
    chestReaderConfigSubmit.disabled = false;
  }
});

chestReaderStop?.addEventListener("click", () => {
  stopChestReader();
  setStatus(checkpointStatus, "Camera scan stopped.");
});

chestReaderCandidates?.addEventListener("click", (event) => {
  const button = event.target.closest("[data-chest-bib]");
  if (!button || !checkpointForm) return;
  const bibNumber = button.dataset.chestBib;
  const participantId = button.dataset.chestParticipantId;
  checkpointForm.elements.namedItem("bibNumber").value = bibNumber;
  if (participantId) {
    checkpointForm.elements.namedItem("participantId").value = participantId;
  }
  stopChestReader();
  setStatus(checkpointStatus, `Recording ${bibNumber} from camera selection...`);
  checkpointForm.requestSubmit();
});

async function scanChestFrame() {
  if (!chestReaderStream || chestReaderBusy || !chestReaderVideo.videoWidth) return;
  chestReaderBusy = true;
  try {
    const blob = await captureChestFrame();
    if (!blob) return;
    const form = new FormData();
    form.append("image", blob, "frame.jpg");
    const response = await fetch(`${basePath}/api/chest-reader/scan`, {
      method: "POST",
      body: form,
    });
    const result = await response.json();
    if (!response.ok) {
      throw new Error(result.error || "Camera scan failed.");
    }
    handleChestReaderResult(result);
  } catch (error) {
    setStatus(checkpointStatus, error.message, "error");
  } finally {
    chestReaderBusy = false;
  }
}

function captureChestFrame() {
  const width = chestReaderVideo.videoWidth || 640;
  const height = chestReaderVideo.videoHeight || 480;
  chestReaderCanvas.width = width;
  chestReaderCanvas.height = height;
  const context = chestReaderCanvas.getContext("2d");
  context.drawImage(chestReaderVideo, 0, 0, width, height);
  return new Promise((resolve) => chestReaderCanvas.toBlob(resolve, "image/jpeg", 0.74));
}

function handleChestReaderResult(result) {
  renderChestReaderCandidates(result.candidates || []);
  if (!result.autoSubmit || !result.bibNumber) {
    chestReaderLastBib = "";
    chestReaderStableCount = 0;
    setStatus(checkpointStatus, result.message || "Select a candidate or type the chest number.");
    return;
  }
  if (result.bibNumber === chestReaderLastBib) {
    chestReaderStableCount += 1;
  } else {
    chestReaderLastBib = result.bibNumber;
    chestReaderStableCount = 1;
  }
  if (chestReaderStableCount < 2) {
    setStatus(checkpointStatus, `Confirming ${result.bibNumber}...`);
    return;
  }
  checkpointForm.elements.namedItem("bibNumber").value = result.bibNumber;
  if (result.participantId) {
    checkpointForm.elements.namedItem("participantId").value = result.participantId;
  }
  stopChestReader();
  setStatus(checkpointStatus, `Recording ${result.bibNumber} from camera...`);
  checkpointForm.requestSubmit();
}

function renderChestReaderCandidates(candidates) {
  if (!chestReaderCandidates) return;
  const registered = candidates.filter((candidate) => candidate.registered);
  const visible = registered.length ? registered : candidates.slice(0, 3);
  if (!visible.length) {
    chestReaderCandidates.innerHTML = "";
    return;
  }
  chestReaderCandidates.innerHTML = visible.map((candidate) => `
    <button type="button" data-chest-bib="${escapeHTML(candidate.bibNumber)}" data-chest-participant-id="${escapeHTML(candidate.participantId || "")}" ${candidate.registered ? "" : "disabled"}>
      <strong>${escapeHTML(candidate.bibNumber)}</strong>
      <span>${candidate.registered ? escapeHTML(candidate.runnerName || "Registered runner") : "Not registered"} · ${Math.round(Number(candidate.confidence || 0) * 100)}%</span>
    </button>
  `).join("");
}

function stopChestReader() {
  if (chestReaderTimer) {
    window.clearInterval(chestReaderTimer);
    chestReaderTimer = null;
  }
  if (chestReaderStream) {
    chestReaderStream.getTracks().forEach((track) => track.stop());
    chestReaderStream = null;
  }
  chestReaderBusy = false;
  chestReaderLastBib = "";
  chestReaderStableCount = 0;
  if (chestReaderVideo) chestReaderVideo.srcObject = null;
  if (chestReaderPreview) chestReaderPreview.hidden = true;
  if (chestReaderStart) chestReaderStart.hidden = false;
  if (chestReaderStop) chestReaderStop.hidden = true;
}

navigationSelects.forEach((select) => {
  select.addEventListener("change", () => {
    const nextURL = select.value;
    if (nextURL) window.location.href = nextURL;
  });
});

analysisButtons.forEach((button) => {
  button.addEventListener("click", async () => {
    const endpoint = button.dataset.analysisEndpoint;
    const target = document.getElementById(button.dataset.analysisTarget);
    if (!endpoint || !target) return;
    const original = button.textContent;
    button.disabled = true;
    button.textContent = "Analyzing...";
    target.innerHTML = `<div class="analysis-placeholder">Reading checkpoint, segment, gap, and leaderboard data...</div>`;
    target.classList.remove("error");
    try {
      const result = await postJSON(endpoint, {});
      renderAnalysis(target, result.analysis);
    } catch (error) {
      target.textContent = error.message;
      target.classList.add("error");
    } finally {
      button.disabled = false;
      button.textContent = original;
    }
  });
});

function renderAnalysis(target, analysis) {
  const parsed = parseAnalysisJSON(analysis);
  if (!parsed) {
    target.innerHTML = `<div class="analysis-card wide"><p>${escapeHTML(analysis)}</p></div>`;
    return;
  }
  const notes = Array.isArray(parsed.staffNotes) ? parsed.staffNotes.slice(0, 3) : [];
  const noteItems = notes.length
    ? notes.map((note) => `<li>${renderAnalysisInline(note)}</li>`).join("")
    : `<li>No staff notes returned.</li>`;
  const riskLevel = analysisPlainText(parsed.riskLevel, "watch").toLowerCase();
  const riskClass = riskLevel.replace(/[^a-z0-9-]/g, "") || "watch";
  target.innerHTML = `
    <article class="analysis-card wide">
      <span>Summary</span>
      <strong>${escapeHTML(analysisPlainText(parsed.summary, "No summary returned."))}</strong>
    </article>
    <article class="analysis-card">
      <span>Performance</span>
      ${renderAnalysisValue(parsed.performance, "Insufficient segment data.")}
    </article>
    <article class="analysis-card">
      <span>Checkpoint Insight</span>
      ${renderAnalysisValue(parsed.checkpointInsight, "No checkpoint insight returned.")}
    </article>
    <article class="analysis-card">
      <span>Gap Insight</span>
      ${renderAnalysisValue(parsed.gapInsight, "No gap insight returned.")}
    </article>
    <article class="analysis-card risk-${escapeHTML(riskClass)}">
      <span>Risk</span>
      <strong>${escapeHTML(riskLevel)}</strong>
    </article>
    <article class="analysis-card wide">
      <span>Next Action</span>
      <strong>${escapeHTML(analysisPlainText(parsed.nextAction, "Keep monitoring the next checkpoint."))}</strong>
    </article>
    <article class="analysis-card wide">
      <span>Staff Notes</span>
      <ul class="analysis-list">${noteItems}</ul>
    </article>
  `;
}

function renderAnalysisValue(value, fallback) {
  if (value === undefined || value === null || value === "") {
    return `<p>${escapeHTML(fallback)}</p>`;
  }
  if (Array.isArray(value)) {
    if (!value.length) return `<p>${escapeHTML(fallback)}</p>`;
    return `<ul class="analysis-list">${value.map((item) => `<li>${renderAnalysisInline(item)}</li>`).join("")}</ul>`;
  }
  if (typeof value === "object") {
    const rows = Object.entries(value)
      .filter(([, item]) => item !== undefined && item !== null && item !== "")
      .map(([key, item]) => `
        <div>
          <dt>${escapeHTML(formatAnalysisLabel(key))}</dt>
          <dd>${renderAnalysisInline(item)}</dd>
        </div>
      `)
      .join("");
    return rows ? `<dl class="analysis-kv-list">${rows}</dl>` : `<p>${escapeHTML(fallback)}</p>`;
  }
  return `<p>${escapeHTML(String(value))}</p>`;
}

function renderAnalysisInline(value) {
  if (value === undefined || value === null || value === "") return "";
  if (Array.isArray(value)) {
    return `<ul class="analysis-list nested">${value.map((item) => `<li>${renderAnalysisInline(item)}</li>`).join("")}</ul>`;
  }
  if (typeof value === "object") {
    return Object.entries(value)
      .filter(([, item]) => item !== undefined && item !== null && item !== "")
      .map(([key, item]) => `<span class="analysis-inline-pair"><b>${escapeHTML(formatAnalysisLabel(key))}</b> ${renderAnalysisInline(item)}</span>`)
      .join("");
  }
  return escapeHTML(String(value));
}

function analysisPlainText(value, fallback) {
  if (value === undefined || value === null || value === "") return fallback;
  if (typeof value === "object") {
    return Object.entries(value)
      .map(([key, item]) => `${formatAnalysisLabel(key)}: ${analysisPlainText(item, "")}`)
      .filter(Boolean)
      .join(" ");
  }
  return String(value);
}

function formatAnalysisLabel(value) {
  return String(value)
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[-_]/g, " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function parseAnalysisJSON(value) {
  if (!value) return null;
  const text = String(value).trim();
  const unfenced = text.replace(/^```json\s*/i, "").replace(/^```\s*/i, "").replace(/```$/i, "").trim();
  try {
    return JSON.parse(unfenced);
  } catch {
    return null;
  }
}

registrationForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(registrationForm);
  setStatus(registrationStatus, "Registering runner...");
  try {
    const participant = await postJSON(`${basePath}/api/participants`, {
      name: form.get("name"),
      phoneNumber: form.get("phoneNumber"),
      category: form.get("category") || "",
      notes: form.get("notes"),
    });
    setStatus(registrationStatus, `${participant.name} registered as ${participant.bibNumber} (${participant.category || "no category"}).`, "success");
    registrationForm.reset();
    await refreshState();
  } catch (error) {
    setStatus(registrationStatus, error.message, "error");
  }
});

startRaceForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  setStatus(startRaceStatus, "Starting race...");
  try {
    const eventData = await postJSON(`${basePath}/api/start-race`, {});
    updateEvent(eventData);
    await refreshState();
    setStatus(startRaceStatus, `${eventData.name} is active. Start checkpoint recorded.`, "success");
  } catch (error) {
    setStatus(startRaceStatus, error.message, "error");
  }
});

eventSettingsForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(eventSettingsForm);
  const localStart = form.get("startTime");
  setStatus(eventSettingsStatus, "Saving race setup...");
  try {
    const eventData = await postJSON(`${basePath}/api/event-settings`, {
      distanceKm: Number(form.get("distanceKm")),
      startTime: new Date(localStart).toISOString(),
    });
    setStatus(eventSettingsStatus, `${eventData.distanceKm} KM start saved.`, "success");
    await refreshState();
  } catch (error) {
    setStatus(eventSettingsStatus, error.message, "error");
  }
});

const raceRows = document.querySelector("#race-rows");
const addRaceRowButton = document.querySelector("#add-race-row");

function syncRemoveRaceButtons() {
  if (!raceRows) return;
  const rows = raceRows.querySelectorAll(".race-row");
  rows.forEach((row) => {
    const remove = row.querySelector("[data-remove-race]");
    if (remove) remove.disabled = rows.length <= 1;
  });
}

addRaceRowButton?.addEventListener("click", () => {
  if (!raceRows) return;
  const first = raceRows.querySelector(".race-row");
  if (!first) return;
  const clone = first.cloneNode(true);
  clone.querySelectorAll("input").forEach((input) => {
    if (input.name === "raceCheckpoints") {
      input.value = "2";
    } else {
      input.value = "";
    }
  });
  raceRows.appendChild(clone);
  syncRemoveRaceButtons();
  clone.querySelector("input")?.focus();
});

raceRows?.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-remove-race]");
  if (!remove) return;
  const rows = raceRows.querySelectorAll(".race-row");
  if (rows.length <= 1) return;
  remove.closest(".race-row")?.remove();
  syncRemoveRaceButtons();
});

syncRemoveRaceButtons();

eventCreateForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const races = [];
  for (const row of raceRows?.querySelectorAll(".race-row") ?? []) {
    const name = row.querySelector("[name='raceName']")?.value.trim();
    const distance = Number(row.querySelector("[name='raceDistance']")?.value);
    const startRaw = row.querySelector("[name='raceStart']")?.value;
    const checkpoints = Number(row.querySelector("[name='raceCheckpoints']")?.value);
    if (!name || !startRaw) continue;
    races.push({
      name,
      distanceKm: distance,
      startTime: new Date(startRaw).toISOString(),
      checkpoints: Number.isFinite(checkpoints) ? checkpoints : 0,
    });
  }
  if (races.length === 0) {
    setStatus(eventCreateStatus, "Add at least one race with a name and start time.", "error");
    return;
  }
  const form = new FormData(eventCreateForm);
  setStatus(eventCreateStatus, "Creating marathon...");
  try {
    const created = await postJSON("/api/events", {
      name: form.get("name"),
      location: form.get("location"),
      races,
    });
    const firstRace = created.races?.[0];
    setStatus(eventCreateStatus, `${created.marathonName} created with ${created.races?.length ?? 0} race(s).`, "success");
    if (firstRace?.id) {
      window.location.href = `/events/${encodeURIComponent(firstRace.id)}`;
    } else {
      window.location.reload();
    }
  } catch (error) {
    setStatus(eventCreateStatus, error.message, "error");
  }
});

volunteerForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(volunteerForm);
  setStatus(volunteerStatus, "Adding volunteer...");
  try {
    const user = await postJSON("/api/volunteers", {
      username: form.get("username"),
      password: form.get("password"),
    });
    setStatus(volunteerStatus, `${user.username} can now log in.`, "success");
    window.location.reload();
  } catch (error) {
    setStatus(volunteerStatus, error.message, "error");
  }
});

document.querySelectorAll("[data-delete-volunteer]").forEach((button) => {
  button.addEventListener("click", async () => {
    const username = button.dataset.deleteVolunteer;
    setStatus(volunteerStatus, `Removing ${username}...`);
    try {
      const response = await fetch(`/api/volunteers/${encodeURIComponent(username)}/delete`, { method: "POST" });
      const body = await response.json();
      if (!response.ok) {
        throw new Error(body.error || "Volunteer could not be removed.");
      }
      setStatus(volunteerStatus, `${username} removed.`, "success");
      window.location.reload();
    } catch (error) {
      setStatus(volunteerStatus, error.message, "error");
    }
  });
});

document.querySelectorAll("[data-delete-event]").forEach((button) => {
  button.addEventListener("click", async () => {
    const eventId = button.dataset.deleteEvent;
    if (!eventId || !window.confirm("Delete this marathon and all runner data?")) return;
    const original = button.textContent;
    button.disabled = true;
    button.textContent = "Deleting...";
    try {
      const response = await fetch(`/api/events/${encodeURIComponent(eventId)}/delete`, { method: "POST" });
      const body = await response.json();
      if (!response.ok) {
        throw new Error(body.error || "Marathon could not be deleted.");
      }
      window.location.href = body.redirect || "/";
    } catch (error) {
      button.disabled = false;
      button.textContent = original;
      setStatus(eventCreateStatus, error.message, "error");
    }
  });
});

document.querySelectorAll("[data-delete-runner]").forEach((button) => {
  button.addEventListener("click", async () => {
    const bibNumber = button.dataset.deleteRunner;
    const endpoint = button.dataset.deleteRunnerEndpoint;
    if (!endpoint || !window.confirm(`Delete runner ${bibNumber} and all checkpoint entries?`)) return;
    const original = button.textContent;
    button.disabled = true;
    button.textContent = "Deleting...";
    try {
      const response = await fetch(endpoint, { method: "POST" });
      const body = await response.json();
      if (!response.ok) {
        throw new Error(body.error || "Runner could not be deleted.");
      }
      window.location.href = body.redirect || "/";
    } catch (error) {
      button.disabled = false;
      button.textContent = original;
      alert(error.message);
    }
  });
});

checkpointManagerForm?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(checkpointManagerForm);
  setStatus(checkpointManagerStatus, "Adding checkpoint...");
  try {
    const checkpoint = await postJSON(`${basePath}/api/checkpoints`, {
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
    const response = await fetch(`${basePath}/api/import-runners`, {
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
  if (!document.querySelector("[data-page='dashboard'], [data-page='race'], [data-page='leaderboard']")) return;
  const response = await fetch(`${basePath}/api/state`);
  if (!response.ok) return;
  const state = await response.json();
  updateEvent(state.event);
  updateStats(state.summary);
  updateCheckpoints(state.checkpoints);
  updateParticipants(state.participants);
  updateFeed(state.liveFeed);
  updateLeaderboard(state);
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
    startNode.textContent = formatTime(eventData.startTime);
  }
  const statusNode = document.querySelector("[data-event-status]");
  if (statusNode) statusNode.textContent = eventData.status;
}

function hydrateDisplayedEventTime() {
  const startNode = document.querySelector("[data-event-start]");
  if (startNode?.dataset.eventStart) {
    startNode.textContent = formatTime(startNode.dataset.eventStart);
  }
}

function formatTime(iso) {
  return new Date(iso).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

function updateStats(summary) {
  const pairs = {
    total: summary.totalParticipants,
    finished: summary.finished,
    active: summary.active,
    dnf: summary.dnf,
    completion: summary.courseProgress ?? summary.completionRate,
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

function updateParticipants(participants) {
  const select = checkpointForm?.elements.namedItem("participantId");
  if (!select) return;
  const selected = select.value;
  if (!participants.length) {
    select.innerHTML = `<option value="">Register a runner first</option>`;
    return;
  }
  select.innerHTML = participants.map((participant) => `
    <option value="${escapeHTML(participant.id)}">${escapeHTML(participant.bibNumber)} · ${escapeHTML(participant.name)}</option>
  `).join("");
  if (selected && [...select.options].some((option) => option.value === selected)) {
    select.value = selected;
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

let _activeLeaderboardCategory = "";
let _lastLeaderboardState = null;

function updateLeaderboard(state) {
  _lastLeaderboardState = state;
  const entries = _activeLeaderboardCategory === ""
    ? (state.leaderboard || [])
    : getCategoryEntries(state, _activeLeaderboardCategory);
  renderLeaderboardEntries(entries);
}

function getCategoryEntries(state, category) {
  const categoryBoards = state.categoryLeaderboards || [];
  const board = categoryBoards.find((b) => b.category === category);
  return board ? board.entries : [];
}

function renderLeaderboardEntries(entries) {
  const body = document.querySelector("#leaderboard-body");
  if (!body) return;
  if (!entries || !entries.length) {
    body.innerHTML = `<tr><td colspan="8" class="empty-state">No runners in this category yet.</td></tr>`;
    return;
  }
  body.innerHTML = entries.map((entry) => `
    <tr data-category="${escapeHTML(entry.category || "")}">
      <td class="rank">#${entry.rank}</td>
      <td>${escapeHTML(entry.bibNumber)}</td>
      <td><a href="${basePath}/runners/${encodeURIComponent(entry.bibNumber)}">${escapeHTML(entry.runnerName)}</a></td>
      <td><span class="category-badge">${escapeHTML(entry.category || "—")}</span></td>
      <td>${escapeHTML(entry.status)}</td>
      <td>${escapeHTML(entry.latestCheckpoint)}</td>
      <td>${escapeHTML(entry.finishTime)}</td>
      <td>${escapeHTML(entry.gap)}</td>
    </tr>
  `).join("");
}

function applyLeaderboardFilter(category) {
  _activeLeaderboardCategory = category;
  if (_lastLeaderboardState) {
    updateLeaderboard(_lastLeaderboardState);
  }
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
hydrateDisplayedEventTime();

// Category filter tabs (leaderboard) — manual selection plus a 10s auto-rotation
// that cycles All → each category → repeat, so a display board surfaces every
// category without anyone clicking.
const leaderboardTabs = Array.from(document.querySelectorAll("#leaderboard-tabs .tab-btn"));
let _leaderboardRotation = null;

function activateLeaderboardTab(btn) {
  if (!btn) return;
  leaderboardTabs.forEach((b) => {
    const isActive = b === btn;
    b.classList.toggle("active", isActive);
    b.setAttribute("aria-selected", isActive ? "true" : "false");
  });
  applyLeaderboardFilter(btn.dataset.category || "");
}

function startLeaderboardRotation() {
  if (_leaderboardRotation || leaderboardTabs.length < 2) return;
  _leaderboardRotation = setInterval(() => {
    const current = leaderboardTabs.findIndex((b) => b.classList.contains("active"));
    const next = leaderboardTabs[(current + 1) % leaderboardTabs.length];
    activateLeaderboardTab(next);
  }, 10000);
}

function stopLeaderboardRotation() {
  if (!_leaderboardRotation) return;
  clearInterval(_leaderboardRotation);
  _leaderboardRotation = null;
}

leaderboardTabs.forEach((btn) => {
  btn.addEventListener("click", () => {
    activateLeaderboardTab(btn);
    // A manual pick gets a fresh dwell rather than jumping at the next tick.
    stopLeaderboardRotation();
    startLeaderboardRotation();
  });
});

// Pause rotation while someone is reading the standings, resume when they leave.
const leaderboardStage = document.querySelector("#leaderboard");
leaderboardStage?.addEventListener("mouseenter", stopLeaderboardRotation);
leaderboardStage?.addEventListener("mouseleave", startLeaderboardRotation);

startLeaderboardRotation();

setInterval(refreshState, 5000);
