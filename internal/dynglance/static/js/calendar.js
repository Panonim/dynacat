import { directions, easeOutQuint, slideFade } from "./animations.js";
import { elem, repeat, text } from "./templating.js";
import { setupPopovers } from "./popover.js";

const FULL_MONTH_SLOTS = 7*6;
const WEEKDAY_ABBRS = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
const MONTH_NAMES = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

const leftArrowSvg = `<svg stroke="var(--color-text-base)" fill="none" viewBox="0 0 24 24" stroke-width="1.5" xmlns="http://www.w3.org/2000/svg">
  <path stroke-linecap="round" stroke-linejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
</svg>`;

const rightArrowSvg = `<svg stroke="var(--color-text-base)" fill="none" viewBox="0 0 24 24" stroke-width="1.5" xmlns="http://www.w3.org/2000/svg">
  <path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
</svg>`;

const undoArrowSvg = `<svg stroke="var(--color-text-base)" fill="none" viewBox="0 0 24 24" stroke-width="1.5" xmlns="http://www.w3.org/2000/svg">
  <path stroke-linecap="round" stroke-linejoin="round" d="M9 15 3 9m0 0 6-6M3 9h12a6 6 0 0 1 0 12h-3" />
</svg>`;

const [datesExitLeft, datesExitRight] = directions(
    slideFade, { distance: "2rem", duration: 120, offset: 1 },
    "left", "right"
);

const [datesEntranceLeft, datesEntranceRight] = directions(
    slideFade, { distance: "0.8rem", duration: 500, easing: easeOutQuint },
    "left", "right"
);

const undoEntrance = slideFade({ direction: "left", distance: "100%", duration: 300 });

export default function(element) {
    // Guard against double-initialization: a built calendar still carries the
    // "calendar" class, so if setup runs again it must not rebuild it (which would
    // create a second instance and replay the month animation).
    if (element.querySelector(".calendar-dates")) return;

    const widgetElement = element.closest("[data-widget-id]");
    const releasesEnabled = element.dataset.calendarReleases === "true" && widgetElement !== null;

    const releases = releasesEnabled
        ? Releases(widgetElement.dataset.widgetId, Number(element.dataset.calendarReleasesInterval) || 0)
        : null;

    element.swapWith(Calendar(
        Number(element.dataset.firstDayOfWeek ?? 1),
        releases
    ));
}

// Releases manages fetching + caching per-month Sonarr/Radarr release data and
// rendering it as markers/popovers inside the calendar day cells.
function Releases(widgetId, intervalMs) {
    const base = (typeof pageData !== "undefined" && pageData.baseURL) || "";
    const cache = new Map();

    const fetchMonth = async (date, force) => {
        const key = monthKey(date);

        if (!force && cache.has(key)) return cache.get(key);

        const year = date.getFullYear();
        const month = date.getMonth() + 1;

        try {
            const resp = await fetch(`${base}/api/widgets/${widgetId}/action/releases/${year}/${month}`, { method: "POST" });
            if (!resp.ok) return cache.get(key) || {};
            const data = await resp.json();
            cache.set(key, data || {});
            return cache.get(key);
        } catch (e) {
            console.error("calendar: failed to fetch releases", e);
            return cache.get(key) || {};
        }
    };

    return {
        intervalMs,
        getCached: (date) => cache.get(monthKey(date)) || {},
        fetchMonth,
        afterRender: setupPopovers,
    };
}

// TODO: when viewing the previous/next month, display the current date if it's within the spill-over days
function Calendar(firstDay, releases) {
    let header, dates;
    let advanceTimeTicker;
    let releaseTicker;
    let now = new Date();
    let activeDate;

    const loadReleases = (date, force) => {
        if (!releases) return;
        releases.fetchMonth(date, force).then(() => {
            if (monthKey(activeDate) === monthKey(date)) {
                dates.component.applyMarkers(activeDate);
            }
        });
    };

    const update = (newDate) => {
        header.component.update(now, newDate);
        dates.component.update(now, newDate);
        activeDate = newDate;
        loadReleases(newDate, false);
    };

    const autoAdvanceNow = () => {
        advanceTimeTicker = setTimeout(() => {
            // TODO: don't auto advance if looking at a different month
            update(now = new Date());
            autoAdvanceNow();
        }, msTillNextDay());
    };

    const adjacentMonth = (dir) => new Date(activeDate.getFullYear(), activeDate.getMonth() + dir, 1);
    const nextClicked = () => update(adjacentMonth(1));
    const prevClicked = () => update(adjacentMonth(-1));
    const undoClicked = () => update(now);

    const calendar = elem().classes("calendar").append(
        header = Header(nextClicked, prevClicked, undoClicked),
        dates = Dates(firstDay, releases)
    );

    update(now);
    autoAdvanceNow();

    // Only poll for live updates when the page has dynamic updates enabled, matching
    // SSE/widget polling. The initial fetch above still runs so markers show either way.
    const dynamicUpdatesEnabled = typeof pageData !== "undefined" && pageData.dynamicUpdateEnabled;
    if (releases && releases.intervalMs > 0 && dynamicUpdatesEnabled) {
        releaseTicker = setInterval(() => loadReleases(activeDate, true), releases.intervalMs);
    }

    return calendar.component({
        suspend: () => {
            clearTimeout(advanceTimeTicker);
            clearInterval(releaseTicker);
        }
    });
}

function Header(nextClicked, prevClicked, undoClicked) {
    let month, monthNumber, year, undo;
    const button = () => elem("button").classes("calendar-header-button");

    const monthAndYear = elem().classes("size-h2", "color-highlight").append(
        month = text(),
        " ",
        year = elem("span").classes("size-h3"),
        undo = button()
            .hide()
            .classes("calendar-undo-button")
            .attr("title", "Back to current month")
            .on("click", undoClicked)
            .html(undoArrowSvg)
    );

    const monthSwitcher = elem()
        .classes("flex", "gap-7", "items-center")
        .append(
            button()
                .attr("title", "Previous month")
                .on("click", prevClicked)
                .html(leftArrowSvg),
            monthNumber = elem()
                .classes("color-highlight")
                .styles({ marginTop: "0.1rem" }),
            button()
                .attr("title", "Next month")
                .on("click", nextClicked)
                .html(rightArrowSvg),
        );

    return elem().classes("flex", "justify-between", "items-center").append(
        monthAndYear,
        monthSwitcher
    ).component({
        update: function (now, newDate) {
            month.text(MONTH_NAMES[newDate.getMonth()]);
            year.text(newDate.getFullYear());
            const m = newDate.getMonth() + 1;
            monthNumber.text((m < 10 ? "0" : "") + m);

            if (!datesWithinSameMonth(now, newDate)) {
                if (undo.isHidden()) undo.show().animate(undoEntrance);
            } else {
                undo.hide();
            }

            return this;
        }
    });
}

function Dates(firstDay, releases) {
    let dates, lastRenderedDate;

    // applyMarkers (re)draws release indicators + popovers onto the day cells for
    // the given month using whatever release data is currently cached.
    const applyMarkers = function(newDate) {
        if (!releases) return;

        const data = releases.getCached(newDate);
        const children = dates.children;

        const firstWeekday = new Date(newDate.getFullYear(), newDate.getMonth(), 1).getDay();
        const previousMonthSpilloverDays = (firstWeekday - firstDay + 7) % 7 || 7;
        const firstCellDate = new Date(newDate.getFullYear(), newDate.getMonth(), 1 - previousMonthSpilloverDays);

        for (let i = 0; i < FULL_MONTH_SLOTS; i++) {
            const cell = children[i];
            const existing = cell.querySelector(".calendar-release");
            if (existing) existing.remove();

            const cellDate = new Date(firstCellDate.getFullYear(), firstCellDate.getMonth(), firstCellDate.getDate() + i);
            const items = data[isoDate(cellDate)];
            if (items && items.length) {
                cell.append(releaseMarker(items));
            }
        }

        releases.afterRender();
    };

    const updateFullMonth = function(now, newDate) {
        const firstWeekday = new Date(newDate.getFullYear(), newDate.getMonth(), 1).getDay();
        const previousMonthSpilloverDays = (firstWeekday - firstDay + 7) % 7 || 7;
        const currentMonthDays = daysInMonth(newDate.getFullYear(), newDate.getMonth());
        const nextMonthSpilloverDays = FULL_MONTH_SLOTS - (previousMonthSpilloverDays + currentMonthDays);
        const previousMonthDays = daysInMonth(newDate.getFullYear(), newDate.getMonth() - 1)
        const isCurrentMonth = datesWithinSameMonth(now, newDate);
        const currentDate = now.getDate();

        let children = dates.children;
        let index = 0;

        for (let i = 0; i < FULL_MONTH_SLOTS; i++) {
            children[i].clearClasses("calendar-spillover-date", "calendar-current-date");
        }

        for (let i = 0; i < previousMonthSpilloverDays; i++, index++) {
            children[index].classes("calendar-spillover-date").text(
                previousMonthDays - previousMonthSpilloverDays + i + 1
            )
        }

        for (let i = 1; i <= currentMonthDays; i++, index++) {
            children[index]
                .classesIf(isCurrentMonth && i === currentDate, "calendar-current-date")
                .text(i);
        }

        for (let i = 0; i < nextMonthSpilloverDays; i++, index++) {
            children[index].classes("calendar-spillover-date").text(i + 1);
        }

        lastRenderedDate = newDate;

        // Day numbers are set via .text() which wipes any previously appended
        // markers, so re-apply them after rendering the grid.
        applyMarkers(newDate);
    };

    const update = function(now, newDate) {
        if (lastRenderedDate === undefined || datesWithinSameMonth(newDate, lastRenderedDate)) {
            updateFullMonth(now, newDate);
            return;
        }

        const next = newDate > lastRenderedDate;
        dates.animateUpdate(
            () => updateFullMonth(now, newDate),
            next ? datesExitLeft : datesExitRight,
            next ? datesEntranceRight : datesEntranceLeft,
        );
    }

    return elem().append(
        elem().classes("calendar-dates", "margin-top-15").append(
            ...repeat(7, (i) => elem().classes("size-h6", "color-subdue").text(
                WEEKDAY_ABBRS[(firstDay + i) % 7]
            ))
        ),

        dates = elem().classes("calendar-dates", "margin-top-3").append(
            ...elem().classes("calendar-date").duplicate(FULL_MONTH_SLOTS)
        )
    ).component({ update, applyMarkers });
}

function releaseMarker(items) {
    const list = elem().classes("list", "list-gap-10");
    for (const item of items) {
        list.append(releaseCard(item));
    }

    return elem()
        .classes("calendar-release")
        .attrs({
            "data-popover-type": "html",
            "data-popover-position": "above",
            "data-popover-max-width": "340px",
        })
        .append(
            elem().classes("calendar-release-indicator"),
            elem().attr("data-popover-html", "").append(list)
        );
}

function releaseCard(item) {
    const body = elem().classes("min-width-0", "flex-1").append(
        elem().classes("size-h6", "color-subdue").text(item.source)
    );

    body.append(elem().classes("color-highlight", "text-truncate").text(item.title));

    if (item.description) {
        body.append(elem().classes("color-base", "text-truncate-2-lines", "margin-top-3").text(item.description));
    }

    const card = (item.link
        ? elem("a").attrs({ href: item.link, target: "_blank", rel: "noreferrer" })
        : elem()
    ).classes("calendar-release-card", "flex", "items-center", "gap-10");

    if (item.thumbnail) {
        card.append(
            elem().classes("calendar-release-thumb", "thumbnail-container").append(
                elem("img").classes("thumbnail").attrs({ src: item.thumbnail, loading: "lazy", alt: "" })
            )
        );
    }

    card.append(body);

    return card;
}

function isoDate(date) {
    const month = date.getMonth() + 1;
    const day = date.getDate();
    return `${date.getFullYear()}-${month < 10 ? "0" : ""}${month}-${day < 10 ? "0" : ""}${day}`;
}

function monthKey(date) {
    return `${date.getFullYear()}-${date.getMonth() + 1}`;
}

function datesWithinSameMonth(d1, d2) {
    return d1.getFullYear() === d2.getFullYear() && d1.getMonth() === d2.getMonth();
}

function daysInMonth(year, month) {
    return new Date(year, month + 1, 0).getDate();
}

function msTillNextDay(now) {
    now = now || new Date();

    return 86_400_000 - (
      now.getMilliseconds() +
      now.getSeconds() * 1000 +
      now.getMinutes() * 60_000 +
      now.getHours() * 3_600_000
    );
}
