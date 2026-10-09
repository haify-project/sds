// Keeps the hero's caption and server labels in step with its 12 s animation
// (the timings are in partials/hero.html), pauses it off screen, and holds
// one frame when the reader has asked for less motion.
(function () {
  var svg = document.getElementById("haify-hero-scene");
  if (!svg) return;
  var caption = document.getElementById("haify-hero-caption");
  var labels = {};
  svg.querySelectorAll(".haify-hero-state").forEach(function (el) { labels[el.dataset.node] = el; });

  // [from second, caption, server 1, server 2, server 3]
  var STEPS = [
    [0, "Apps use one address. Every write is kept on more than one server.", "serving", "in sync", "in sync"],
    [4.5, "server 1 fails. server 2 takes over.", "offline", "taking over", "in sync"],
    [6, "Apps carry on, with every write they made.", "offline", "serving", "in sync"],
    [10, "server 1 is back and catches up on its own.", "catching up", "serving", "in sync"],
    [10.8, "server 1 is back and catches up on its own.", "in sync", "serving", "in sync"]
  ];
  var shown = -1;

  function render() {
    var t = svg.getCurrentTime() % 12, i = 0;
    while (i + 1 < STEPS.length && t >= STEPS[i + 1][0]) i++;
    if (i === shown) return;
    shown = i;
    caption.textContent = STEPS[i][1];
    ["1", "2", "3"].forEach(function (n, k) {
      var state = STEPS[i][k + 2];
      labels[n].textContent = state;
      labels[n].setAttribute("class", "haify-hero-state is-" + state.replace(" ", "-"));
    });
  }

  function tick() { render(); requestAnimationFrame(tick); }

  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  document.getElementById("haify-replay").addEventListener("click", function () {
    svg.setCurrentTime(still ? parseFloat(svg.dataset.still) : 0);
    render();
  });
  if (still) {
    svg.pauseAnimations();
    svg.setCurrentTime(parseFloat(svg.dataset.still));
    render();
    return;
  }
  if ("IntersectionObserver" in window) {
    new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { e.isIntersecting ? svg.unpauseAnimations() : svg.pauseAnimations(); });
    }).observe(svg);
  }
  tick();
})();
