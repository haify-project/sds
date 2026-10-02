// The home page's replication panel: writes land on the Primary and are
// mirrored to both peers, the Primary goes away, drbd-reactor promotes a peer
// and writes carry on there. It plays once; with reduced motion the final
// state is drawn straight away.
(function () {
  var panel = document.querySelector(".sds-cluster");
  if (!panel) return;

  var BLOCKS = 12;
  var nodes = {};
  panel.querySelectorAll(".sds-node").forEach(function (el) {
    var strip = el.querySelector(".sds-blocks");
    strip.innerHTML = "";
    for (var i = 0; i < BLOCKS; i++) strip.appendChild(document.createElement("i"));
    nodes[el.dataset.node] = { el: el, cells: strip.children };
  });
  var statusEl = document.getElementById("sds-status");
  var captionEl = document.getElementById("sds-cluster-caption");
  var timers = [];

  var STATES = {
    start: { node1: ["Primary", "UpToDate", ""], node2: ["Secondary", "UpToDate", ""], node3: ["Secondary", "UpToDate", ""] },
    lost: { node1: ["Unknown", "DUnknown", "gone"], node2: ["Secondary", "UpToDate", ""], node3: ["Secondary", "UpToDate", ""] },
    promoting: { node1: ["Unknown", "DUnknown", "gone"], node2: ["Promoting", "UpToDate", "promoting"], node3: ["Secondary", "UpToDate", ""] },
    moved: { node1: ["Unknown", "DUnknown", "gone"], node2: ["Primary", "UpToDate", ""], node3: ["Secondary", "UpToDate", ""] }
  };

  function show(name, caption) {
    var s = STATES[name], lines = ["$ sds resource status data", "  Node states:"];
    Object.keys(s).forEach(function (n) {
      var role = s[n][0], disk = s[n][1], state = s[n][2], el = nodes[n].el;
      el.dataset.role = role === "Promoting" ? "Secondary" : role;
      el.dataset.state = state;
      el.querySelector(".sds-node__role").innerHTML = "<b>" + role + "</b>" + disk;
      var shownRole = role === "Promoting" ? "Secondary" : role;
      var line = "    " + n + ": role=" + shownRole + " disk=" + disk;
      if (state === "gone") line = '<span class="t">' + line + "</span>";
      else if (shownRole === "Primary") line = '<span class="o">' + line + "</span>";
      lines.push(line);
    });
    statusEl.innerHTML = lines.join("\n");
    if (caption !== undefined) captionEl.textContent = caption;
  }

  function write(primary, peers, i) {
    nodes[primary].cells[i].className = "w";
    peers.forEach(function (p) {
      timers.push(setTimeout(function () { nodes[p].cells[i].className = "w"; }, 140));
    });
  }

  function reset() {
    timers.forEach(clearTimeout);
    timers = [];
    Object.keys(nodes).forEach(function (n) {
      Array.prototype.forEach.call(nodes[n].cells, function (c) { c.className = ""; });
    });
  }

  function at(ms, fn) { timers.push(setTimeout(fn, ms)); }

  function finalState() {
    reset();
    for (var i = 0; i < BLOCKS; i++) {
      if (i < 6) { ["node1", "node2", "node3"].forEach(function (n) { nodes[n].cells[i].className = "w"; }); }
      else { ["node2", "node3"].forEach(function (n) { nodes[n].cells[i].className = "w"; }); }
    }
    show("moved", "node1 failed. drbd-reactor promoted node2, which held every acknowledged write, and the volume kept serving. When node1 returns it resyncs only the blocks that changed.");
  }

  function play() {
    reset();
    show("start", "Each write is acknowledged once node2 and node3 have it too.");
    var t = 500;
    for (var i = 0; i < 6; i++) { (function (i) { at(t, function () { write("node1", ["node2", "node3"], i); }); })(i); t += 420; }
    at(t + 400, function () { show("lost", "node1 stops answering. node2 and node3 still hold quorum."); });
    at(t + 2000, function () { show("promoting", "drbd-reactor promotes node2. Its copy is UpToDate, so nothing acknowledged is lost."); });
    at(t + 3400, function () { show("moved", "Writes continue on node2. When node1 returns it resyncs only the blocks that changed."); });
    t += 3800;
    for (var j = 6; j < BLOCKS; j++) { (function (j) { at(t, function () { write("node2", ["node3"], j); }); })(j); t += 420; }
  }

  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  document.getElementById("sds-replay").addEventListener("click", function () { still ? finalState() : play(); });
  if (still) finalState(); else play();
})();
