// The home page's panel: writes land on node1 and are copied to the other two,
// node1 goes away, node2 takes over and writes carry on there. It plays once;
// with reduced motion the final state is drawn straight away.
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
  var captionEl = document.getElementById("sds-cluster-caption");
  var timers = [];

  // node -> [label, state]; state drives the colour.
  var STATES = {
    start: { node1: ["Serving", "serving"], node2: ["In sync", ""], node3: ["In sync", ""] },
    lost: { node1: ["Offline", "gone"], node2: ["In sync", ""], node3: ["In sync", ""] },
    takeover: { node1: ["Offline", "gone"], node2: ["Taking over", "turning"], node3: ["In sync", ""] },
    moved: { node1: ["Offline", "gone"], node2: ["Serving", "serving"], node3: ["In sync", ""] }
  };

  function show(name, caption) {
    var s = STATES[name];
    Object.keys(s).forEach(function (n) {
      nodes[n].el.dataset.state = s[n][1];
      nodes[n].el.querySelector(".sds-node__state").textContent = s[n][0];
    });
    captionEl.textContent = caption;
  }

  function write(from, to, i) {
    nodes[from].cells[i].className = "w";
    to.forEach(function (p) {
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
      var on = i < 6 ? ["node1", "node2", "node3"] : ["node2", "node3"];
      on.forEach(function (n) { nodes[n].cells[i].className = "w"; });
    }
    show("moved", "node1 went offline and node2 took over with every saved write. When node1 is back it catches up on its own.");
  }

  function play() {
    reset();
    show("start", "Each write is saved on all three servers.");
    var t = 500;
    for (var i = 0; i < 6; i++) { (function (i) { at(t, function () { write("node1", ["node2", "node3"], i); }); })(i); t += 420; }
    at(t + 400, function () { show("lost", "node1 goes offline."); });
    at(t + 2000, function () { show("takeover", "node2 takes over. It already has every saved write."); });
    at(t + 3400, function () { show("moved", "Writes continue on node2. When node1 is back it catches up on its own."); });
    t += 3800;
    for (var j = 6; j < BLOCKS; j++) { (function (j) { at(t, function () { write("node2", ["node3"], j); }); })(j); t += 420; }
  }

  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  document.getElementById("sds-replay").addEventListener("click", function () { still ? finalState() : play(); });
  if (still) finalState(); else play();
})();
