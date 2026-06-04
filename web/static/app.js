const checkpointForm = document.querySelector("#checkpoint-form");
const registrationForm = document.querySelector("#registration-form");
const checkpointStatus = document.querySelector("#checkpoint-status");
const registrationStatus = document.querySelector("#registration-status");

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

async function refreshState() {
  if (!document.querySelector("[data-page='dashboard']")) return;
  const response = await fetch("/api/state");
  if (!response.ok) return;
  const state = await response.json();
  updateStats(state.summary);
  updateFeed(state.liveFeed);
  updateLeaderboard(state.leaderboard);
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

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#039;",
  }[char]));
}

setInterval(refreshState, 5000);
