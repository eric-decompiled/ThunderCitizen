// Route detail: HTMX afterSwap handler for badge color re-application
(function () {
  'use strict';

  // After HTMX swaps in schedule content (day picker clicks)
  document.body.addEventListener('htmx:afterSwap', function (e) {
    // Day picker: update active button state
    const picker = e.detail.elt && e.detail.elt.closest && e.detail.elt.closest('.route-day-picker');
    if (picker) {
      const btns = picker.querySelectorAll('.route-day-btn');
      for (let i = 0; i < btns.length; i++) btns[i].classList.remove('route-day-active');
      if (e.detail.elt.classList) e.detail.elt.classList.add('route-day-active');
    }

    // Scroll hint: check if schedule is scrollable
    const isSchedule = e.detail.target.classList.contains('route-schedule-body') ||
      e.detail.target.classList.contains('route-detail-content');
    if (isSchedule) {
      document.querySelectorAll('.route-tp-scroll').forEach(function (el) {
        const wrap = el.closest('.route-tp-scroll-wrap');
        if (!wrap) return;
        function check() { wrap.classList.toggle('scrolled-end', el.scrollLeft + el.clientWidth >= el.scrollWidth - 4); }
        el.addEventListener('scroll', check);
        check();
      });
    }
  });
})();
