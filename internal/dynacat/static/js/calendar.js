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

const releaseTypes = {
    digital: {
        label: "Digital release",
        path: "M21,16H3V4H21M21,2H3C1.89,2 1,2.89 1,4V16A2,2 0 0,0 3,18H10V20H8V22H16V20H14V18H21A2,2 0 0,0 23,16V4C23,2.89 22.1,2 21,2Z",
    },
    physical: {
        label: "Physical release",
        path: "M5,3C3.89,3 3,3.89 3,5V19A2,2 0 0,0 5,21H19A2,2 0 0,0 21,19V5C21,3.89 20.1,3 19,3H5M12,5C15.09,5 17.82,7.04 18.7,10H16A1,1 0 0,0 15,11V13A1,1 0 0,0 16,14H18.71C17.82,16.97 15.09,19 12,19A7,7 0 0,1 5,12A7,7 0 0,1 12,5M12,10A2,2 0 0,0 10,12A2,2 0 0,0 12,14A2,2 0 0,0 14,12A2,2 0 0,0 12,10Z",
    },
    cinema: {
        label: "In cinemas",
        path: "M20.84 2.18L16.91 2.96L19.65 6.5L21.62 6.1L20.84 2.18M13.97 3.54L12 3.93L14.75 7.46L16.71 7.07L13.97 3.54M9.07 4.5L7.1 4.91L9.85 8.44L11.81 8.05L9.07 4.5M4.16 5.5L3.18 5.69C2.1 5.9 1.39 6.96 1.61 8.04L2 10L6.9 9.03L4.16 5.5M20 12V20H4V12H20M22 10H2V20C2 21.11 2.9 22 4 22H20C21.11 22 22 21.11 22 20V10Z",
    },
    episode: {
        label: "Episode",
        path: "M8.16,3L6.75,4.41L9.34,7H4C2.89,7 2,7.89 2,9V19C2,20.11 2.89,21 4,21H20C21.11,21 22,20.11 22,19V9C22,7.89 21.11,7 20,7H14.66L17.25,4.41L15.84,3L12,6.84L8.16,3M4,9H17V19H4V9M19.5,9A1,1 0 0,1 20.5,10A1,1 0 0,1 19.5,11A1,1 0 0,1 18.5,10A1,1 0 0,1 19.5,9M19.5,12A1,1 0 0,1 20.5,13A1,1 0 0,1 19.5,14A1,1 0 0,1 18.5,13A1,1 0 0,1 19.5,12Z",
    },
};

export default function(element) {
    if (element.querySelector(".calendar-dates")) return;

    const widgetElement = element.closest("[data-widget-id]");
    const releasesEnabled = element.dataset.calendarReleases === "true" && widgetElement !== null;

    const releases = releasesEnabled
        ? Releases(widgetElement.dataset.widgetId, Number(element.dataset.calendarReleasesInterval) || 0)
        : null;

    const showReleaseState = element.dataset.calendarReleaseState === "true";

    element.swapWith(Calendar(
        Number(element.dataset.firstDayOfWeek ?? 1),
        releases,
        showReleaseState
    ));
}

function aggregateReleaseState(items) {
    let hasReleased = false, hasUpcoming = false;
    for (const item of items) {
        if (item.state === "released") hasReleased = true;
        else if (item.state === "upcoming") hasUpcoming = true;
    }
    if (hasReleased) return "released";
    if (hasUpcoming) return "upcoming";
    return "available";
}

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
function Calendar(firstDay, releases, showReleaseState) {
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
        dates = Dates(firstDay, releases, showReleaseState)
    );

    update(now);
    autoAdvanceNow();

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

function Dates(firstDay, releases, showReleaseState) {
    let dates, lastRenderedDate, animating = false;

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
            if (cellDate.getMonth() !== newDate.getMonth() || cellDate.getFullYear() !== newDate.getFullYear()) {
                continue;
            }

            const items = data[isoDate(cellDate)];
            if (items && items.length) {
                cell.append(releaseMarker(items, showReleaseState));
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

        // .text() wipes appended markers, so re-apply after rendering the grid.
        applyMarkers(newDate);
    };

    const update = function(now, newDate) {
        if (lastRenderedDate === undefined || datesWithinSameMonth(newDate, lastRenderedDate) || animating) {
            updateFullMonth(now, newDate);
            return;
        }

        const next = newDate > lastRenderedDate;
        animating = true;
        dates.animate(next ? datesExitLeft : datesExitRight, () => {
            updateFullMonth(now, newDate);
            dates.animate(next ? datesEntranceRight : datesEntranceLeft, () => { animating = false; });
        });
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

function releaseMarker(items, showReleaseState) {
    const list = elem().classes("list", "list-gap-10");
    for (const item of items) {
        list.append(releaseCard(item));
    }

    const indicator = elem().classes("calendar-release-indicator");
    if (showReleaseState) {
        indicator.classes("calendar-release-indicator-" + aggregateReleaseState(items));
    }

    return elem()
        .classes("calendar-release")
        .attrs({
            "data-popover-type": "html",
            "data-popover-position": "above",
            "data-popover-max-width": "340px",
            "data-popover-hide-delay": "80",
        })
        .append(
            indicator,
            elem().attr("data-popover-html", "").append(list)
        );
}

function releaseCard(item) {
    const body = elem().classes("min-width-0", "flex-1").append(
        elem().classes("size-h6", "color-subdue").text(item.source)
    );

    const titleRow = elem().classes("flex", "items-center", "gap-7", "min-width-0");
    const type = releaseTypes[item.type];
    if (type) {
        titleRow.append(
            elem().classes("calendar-release-type-icon")
                .attr("title", type.label)
                .html(`<svg viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="0.9" stroke-linejoin="round"><path d="${type.path}"/></svg>`)
        );
    }
    titleRow.append(elem().classes("color-highlight", "text-truncate").text(item.title));
    body.append(titleRow);

    if (item.description) {
        body.append(elem().classes("color-base", "text-truncate-2-lines", "margin-top-3").text(item.description));
    }

    const card = (item.link
        ? elem("a").attrs({ href: item.link, target: "_blank", rel: "noreferrer" })
        : elem()
    ).classes("calendar-release-card", "flex", "items-center", "gap-10");

    if (item.thumbnail) {
        const spinner = elem().classes("calendar-thumb-spinner");
        const img = elem("img").classes("thumbnail").attrs({ src: item.thumbnail, loading: "lazy", alt: "" });

        const reveal = (cls) => { img.classes(cls); spinner.hide(); };
        img.on("load", () => reveal("loaded")).on("error", () => spinner.hide());
        if (img.complete) img.naturalWidth > 0 ? reveal("cached") : spinner.hide();

        card.append(
            elem().classes("calendar-release-thumb", "thumbnail-container").append(spinner, img)
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
