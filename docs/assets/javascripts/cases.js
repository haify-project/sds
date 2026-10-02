// The use-case illustrations: animate only while on screen, and hold one
// telling frame when the reader has asked for less motion.
(function () {
  var svgs = document.querySelectorAll(".sds-cases .sds-ill");
  if (!svgs.length) return;

  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  svgs.forEach(function (svg) {
    svg.pauseAnimations();
    if (still) svg.setCurrentTime(parseFloat(svg.dataset.still) || 0);
  });
  if (still || !("IntersectionObserver" in window)) {
    if (!still) svgs.forEach(function (svg) { svg.unpauseAnimations(); });
    return;
  }

  var seen = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (e.isIntersecting) e.target.unpauseAnimations();
      else e.target.pauseAnimations();
    });
  }, { threshold: 0.2 });
  svgs.forEach(function (svg) { seen.observe(svg); });
})();
