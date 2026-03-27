import { setupPopovers } from './popover.js';
import { setupMasonries } from './masonry.js';
import { throttledDebounce, isElementVisible, openURLInNewTab } from './utils.js';
import { elem, find, findAll } from './templating.js';

async function fetchPageContent(pageData) {
    // TODO: handle non 200 status codes/time outs
    // TODO: add retries
    const response = await fetch(`${pageData.baseURL}/api/pages/${pageData.slug}/content/`);
    const content = await response.text();

    return {
        content,
        isCacheBuilding: response.headers.get("X-Dynacat-Cache-Building") === "true",
    };
}

function setupCarousels() {
    const carouselElements = document.getElementsByClassName("carousel-container");

    if (carouselElements.length == 0) {
        return;
    }

    for (let i = 0; i < carouselElements.length; i++) {
        const carousel = carouselElements[i];

        if (carousel.dataset.initialized) continue;
        carousel.dataset.initialized = "true";

        carousel.classList.add("show-right-cutoff");
        const itemsContainer = carousel.getElementsByClassName("carousel-items-container")[0];

        const determineSideCutoffs = () => {
            if (itemsContainer.scrollLeft != 0) {
                carousel.classList.add("show-left-cutoff");
            } else {
                carousel.classList.remove("show-left-cutoff");
            }

            if (Math.ceil(itemsContainer.scrollLeft) + itemsContainer.clientWidth < itemsContainer.scrollWidth) {
                carousel.classList.add("show-right-cutoff");
            } else {
                carousel.classList.remove("show-right-cutoff");
            }
        }

        const determineSideCutoffsRateLimited = throttledDebounce(determineSideCutoffs, 20, 100);

        itemsContainer.addEventListener("scroll", determineSideCutoffsRateLimited);
        window.addEventListener("resize", determineSideCutoffsRateLimited);

        afterContentReady(determineSideCutoffs);
    }
}

const minuteInSeconds = 60;
const hourInSeconds = minuteInSeconds * 60;
const dayInSeconds = hourInSeconds * 24;
const monthInSeconds = dayInSeconds * 30.4;
const yearInSeconds = dayInSeconds * 365;

function timestampToRelativeTime(timestamp) {
    let delta = Math.round((Date.now() / 1000) - timestamp);
    let prefix = "";

    if (delta < 0) {
        delta = -delta;
        prefix = "in ";
    }

    if (delta < minuteInSeconds) {
        return prefix + "1m";
    }
    if (delta < hourInSeconds) {
        return prefix + Math.floor(delta / minuteInSeconds) + "m";
    }
    if (delta < dayInSeconds) {
        return prefix + Math.floor(delta / hourInSeconds) + "h";
    }
    if (delta < monthInSeconds) {
        return prefix + Math.floor(delta / dayInSeconds) + "d";
    }
    if (delta < yearInSeconds) {
        return prefix + Math.floor(delta / monthInSeconds) + "mo";
    }

    return prefix + Math.floor(delta / yearInSeconds) + "y";
}

function updateRelativeTimeForElements(elements)
{
    for (let i = 0; i < elements.length; i++)
    {
        const element = elements[i];
        const timestamp = element.dataset.dynamicRelativeTime;

        if (timestamp === undefined)
            continue

        element.textContent = timestampToRelativeTime(timestamp);
    }
}

function setupSearchBoxes() {
    const searchWidgets = document.getElementsByClassName("search");

    if (searchWidgets.length == 0) {
        return;
    }

    for (let i = 0; i < searchWidgets.length; i++) {
        const widget = searchWidgets[i];
        const defaultSearchUrl = widget.dataset.defaultSearchUrl;
        const target = widget.dataset.target || "_blank";
        const newTab = widget.dataset.newTab === "true";
        const inputElement = widget.getElementsByClassName("search-input")[0];
        const bangElement = widget.getElementsByClassName("search-bang")[0];
        const bangs = widget.querySelectorAll(".search-bangs > input");
        const bangsMap = {};
        const kbdElement = widget.getElementsByTagName("kbd")[0];
        let currentBang = null;
        let lastQuery = "";

        for (let j = 0; j < bangs.length; j++) {
            const bang = bangs[j];
            bangsMap[bang.dataset.shortcut] = bang;
        }

        const handleKeyDown = (event) => {
            if (event.key == "Escape") {
                inputElement.blur();
                return;
            }

            if (event.key == "Enter") {
                const input = inputElement.value.trim();
                let query;
                let searchUrlTemplate;

                if (currentBang != null) {
                    query = input.slice(currentBang.dataset.shortcut.length + 1);
                    searchUrlTemplate = currentBang.dataset.url;
                } else {
                    query = input;
                    searchUrlTemplate = defaultSearchUrl;
                }
                if (query.length == 0 && currentBang == null) {
                    return;
                }

                const url = searchUrlTemplate.replace("!QUERY!", encodeURIComponent(query));

                if (newTab && !event.ctrlKey || !newTab && event.ctrlKey) {
                    window.open(url, target).focus();
                } else {
                    window.location.href = url;
                }

                lastQuery = query;
                inputElement.value = "";

                return;
            }

            if (event.key == "ArrowUp" && lastQuery.length > 0) {
                inputElement.value = lastQuery;
                return;
            }
        };

        const changeCurrentBang = (bang) => {
            currentBang = bang;
            bangElement.textContent = bang != null ? bang.dataset.title : "";
        }

        const handleInput = (event) => {
            const value = event.target.value.trim();
            if (value in bangsMap) {
                changeCurrentBang(bangsMap[value]);
                return;
            }

            const words = value.split(" ");
            if (words.length >= 2 && words[0] in bangsMap) {
                changeCurrentBang(bangsMap[words[0]]);
                return;
            }

            changeCurrentBang(null);
        };

        inputElement.addEventListener("focus", () => {
            document.addEventListener("keydown", handleKeyDown);
            document.addEventListener("input", handleInput);
        });
        inputElement.addEventListener("blur", () => {
            document.removeEventListener("keydown", handleKeyDown);
            document.removeEventListener("input", handleInput);
        });

        document.addEventListener("keydown", (event) => {
            if (['INPUT', 'TEXTAREA'].includes(document.activeElement.tagName)) return;
            if (event.code != "KeyS") return;

            inputElement.focus();
            event.preventDefault();
        });

        kbdElement.addEventListener("mousedown", () => {
            requestAnimationFrame(() => inputElement.focus());
        });
    }
}

function setupDynamicRelativeTime() {
    // Always do an immediate update pass (new elements may have arrived)
    updateRelativeTimeForElements(document.querySelectorAll("[data-dynamic-relative-time]"));

    if (dynamicRelativeTimeInitialized) return;
    dynamicRelativeTimeInitialized = true;

    const updateInterval = 60 * 1000;
    let lastUpdateTime = Date.now();

    const updateElementsAndTimestamp = () => {
        updateRelativeTimeForElements(document.querySelectorAll("[data-dynamic-relative-time]"));
        lastUpdateTime = Date.now();
    };

    const scheduleRepeatingUpdate = () => setInterval(updateElementsAndTimestamp, updateInterval);

    if (document.hidden === undefined) {
        scheduleRepeatingUpdate();
        return;
    }

    let timeout = scheduleRepeatingUpdate();

    document.addEventListener("visibilitychange", () => {
        if (document.hidden) {
            clearTimeout(timeout);
            return;
        }

        const delta = Date.now() - lastUpdateTime;

        if (delta >= updateInterval) {
            updateElementsAndTimestamp();
            timeout = scheduleRepeatingUpdate();
            return;
        }

        timeout = setTimeout(() => {
            updateElementsAndTimestamp();
            timeout = scheduleRepeatingUpdate();
        }, updateInterval - delta);
    });
}

function setupGroups() {
    const groups = document.getElementsByClassName("widget-type-group");

    if (groups.length == 0) {
        return;
    }

    for (let g = 0; g < groups.length; g++) {
        const group = groups[g];

        if (group.dataset.initialized) continue;
        group.dataset.initialized = "true";

        const titles = group.getElementsByClassName("widget-header")[0].children;
        const tabs = group.getElementsByClassName("widget-group-contents")[0].children;
        let current = 0;

        for (let t = 0; t < titles.length; t++) {
            const title = titles[t];

            if (title.dataset.titleUrl !== undefined) {
                title.addEventListener("mousedown", (event) => {
                    if (event.button != 1) {
                        return;
                    }

                    openURLInNewTab(title.dataset.titleUrl, false);
                    event.preventDefault();
                });
            }

            title.addEventListener("click", () => {
                if (t == current) {
                    if (title.dataset.titleUrl !== undefined) {
                        openURLInNewTab(title.dataset.titleUrl);
                    }

                    return;
                }

                for (let i = 0; i < titles.length; i++) {
                    titles[i].classList.remove("widget-group-title-current");
                    titles[i].setAttribute("aria-selected", "false");
                    tabs[i].classList.remove("widget-group-content-current");
                    tabs[i].setAttribute("aria-hidden", "true");
                }

                if (current < t) {
                    tabs[t].dataset.direction = "right";
                } else {
                    tabs[t].dataset.direction = "left";
                }

                current = t;

                title.classList.add("widget-group-title-current");
                title.setAttribute("aria-selected", "true");
                tabs[t].classList.add("widget-group-content-current");
                tabs[t].style.animation = '';
                tabs[t].setAttribute("aria-hidden", "false");
            });
        }
    }
}

function setupLazyImages() {
    const images = document.querySelectorAll("img[loading=lazy]");

    if (images.length == 0) {
        return;
    }

    function imageFinishedTransition(image) {
        image.classList.add("finished-transition");
    }

    const processImages = () => {
        setTimeout(() => {
            for (let i = 0; i < images.length; i++) {
                const image = images[i];

                if (image.dataset.lazyInitialized) continue;
                image.dataset.lazyInitialized = "true";

                const handleLoad = () => {
                    image.classList.add("loaded");
                    setTimeout(() => imageFinishedTransition(image), 400);
                };

                const handleError = () => {
                    const fallbackSrc = image.dataset.fallbackSrc;
                    if (fallbackSrc && image.src !== fallbackSrc) {
                        image.src = fallbackSrc;
                    }
                };

                if (image.complete) {
                    // Check if the image loaded successfully
                    if (image.naturalHeight > 0) {
                        image.classList.add("cached");
                        setTimeout(() => imageFinishedTransition(image), 1);
                    } else {
                        // Image failed to load, try fallback
                        handleError();
                    }
                } else {
                    image.addEventListener("load", handleLoad);
                    image.addEventListener("error", handleError);
                }
            }
        }, 1);
    };

    if (pageSetupComplete) {
        processImages();
    } else {
        afterContentReady(processImages);
    }
}

function getCollapsibleContainerStates(element) {
    const allContainers = [...element.querySelectorAll('.collapsible-container')];
    return allContainers.map((container) => container.classList.contains('container-expanded'));
}

function restoreCollapsibleContainerStates(element, containerStates) {
    if (!containerStates.length) return;

    const allContainers = [...element.querySelectorAll('.collapsible-container')];

    for (let index = 0; index < containerStates.length; index++) {
        const container = allContainers[index];
        if (!container) continue;

        const button = container.nextElementSibling;
        if (button && button.classList.contains('expand-toggle-button')) {
            const shouldBeExpanded = containerStates[index];
            const isExpanded = container.classList.contains('container-expanded');

            if (isExpanded === shouldBeExpanded) {
                continue;
            }

            container.classList.add('no-reveal-animation');
            button.click();
        }
    }
}

function attachExpandToggleButton(collapsibleContainer) {
    const showMoreText = "Show more";
    const showLessText = "Show less";

    let expanded = false;
    const button = document.createElement("button");
    const icon = document.createElement("span");
    icon.classList.add("expand-toggle-button-icon");
    const textNode = document.createTextNode(showMoreText);
    button.classList.add("expand-toggle-button");
    button.append(textNode, icon);
    button.addEventListener("click", () => {
        expanded = !expanded;

        if (expanded) {
            collapsibleContainer.classList.add("container-expanded");
            button.classList.add("container-expanded");
            textNode.nodeValue = showLessText;
            return;
        }

        const topBefore = button.getClientRects()[0].top;

        collapsibleContainer.classList.remove("no-reveal-animation");
        collapsibleContainer.classList.remove("container-expanded");
        button.classList.remove("container-expanded");
        textNode.nodeValue = showMoreText;

        const topAfter = button.getClientRects()[0].top;

        if (topAfter > 0)
            return;

        window.scrollBy({
            top: topAfter - topBefore,
            behavior: "instant"
        });
    });

    collapsibleContainer.after(button);

    return button;
};


function setupCollapsibleLists() {
    const collapsibleLists = document.querySelectorAll(".list.collapsible-container");

    if (collapsibleLists.length == 0) {
        return;
    }

    for (let i = 0; i < collapsibleLists.length; i++) {
        const list = collapsibleLists[i];

        if (list.dataset.collapseAfter === undefined) {
            continue;
        }

        const collapseAfter = parseInt(list.dataset.collapseAfter);

        if (collapseAfter == -1) {
            continue;
        }

        if (list.children.length <= collapseAfter) {
            continue;
        }

        if (list.nextElementSibling && list.nextElementSibling.classList.contains("expand-toggle-button")) continue;

        attachExpandToggleButton(list);

        for (let c = collapseAfter; c < list.children.length; c++) {
            const child = list.children[c];
            child.classList.add("collapsible-item");
            child.style.animationDelay = ((c - collapseAfter) * 20).toString() + "ms";
        }
    }
}

function setupCollapsibleGrids() {
    const collapsibleGridElements = document.querySelectorAll(".cards-grid.collapsible-container");

    if (collapsibleGridElements.length == 0) {
        return;
    }

    for (let i = 0; i < collapsibleGridElements.length; i++) {
        const gridElement = collapsibleGridElements[i];

        if (gridElement.dataset.collapseAfterRows === undefined) {
            continue;
        }

        const collapseAfterRows = parseInt(gridElement.dataset.collapseAfterRows);

        if (collapseAfterRows == -1) {
            continue;
        }

        if (gridElement.nextElementSibling && gridElement.nextElementSibling.classList.contains("expand-toggle-button")) continue;

        const getCardsPerRow = () => {
            return parseInt(getComputedStyle(gridElement).getPropertyValue('--cards-per-row'));
        };

        const button = attachExpandToggleButton(gridElement);

        let cardsPerRow;

        const resolveCollapsibleItems = () => requestAnimationFrame(() => {
            const hideItemsAfterIndex = cardsPerRow * collapseAfterRows;

            if (hideItemsAfterIndex >= gridElement.children.length) {
                button.style.display = "none";
            } else {
                button.style.removeProperty("display");
            }

            let row = 0;

            for (let i = 0; i < gridElement.children.length; i++) {
                const child = gridElement.children[i];

                if (i >= hideItemsAfterIndex) {
                    child.classList.add("collapsible-item");
                    child.style.animationDelay = (row * 40).toString() + "ms";

                    if (i % cardsPerRow + 1 == cardsPerRow) {
                        row++;
                    }
                } else {
                    child.classList.remove("collapsible-item");
                    child.style.removeProperty("animation-delay");
                }
            }
        });

        const observer = new ResizeObserver(() => {
            if (!isElementVisible(gridElement)) {
                return;
            }

            const newCardsPerRow = getCardsPerRow();

            if (cardsPerRow == newCardsPerRow) {
                return;
            }

            cardsPerRow = newCardsPerRow;
            resolveCollapsibleItems();
        });

        afterContentReady(() => observer.observe(gridElement));
    }
}

const contentReadyCallbacks = [];
let pageSetupComplete = false;
let dynamicRelativeTimeInitialized = false;

function afterContentReady(callback) {
    contentReadyCallbacks.push(callback);
}

const weekDayNames = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const monthNames = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

function makeSettableTimeElement(element, hourFormat) {
    const fragment = document.createDocumentFragment();
    const hour = document.createElement('span');
    const minute = document.createElement('span');
    const amPm = document.createElement('span');
    fragment.append(hour, document.createTextNode(':'), minute);

    if (hourFormat == '12h') {
        fragment.append(document.createTextNode(' '), amPm);
    }

    element.append(fragment);

    return (date) => {
        const hours = date.getHours();

        if (hourFormat == '12h') {
            amPm.textContent = hours < 12 ? 'AM' : 'PM';
            hour.textContent = hours % 12 || 12;
        } else {
            hour.textContent = hours < 10 ? '0' + hours : hours;
        }

        const minutes = date.getMinutes();
        minute.textContent = minutes < 10 ? '0' + minutes : minutes;
    };
};

function timeInZone(now, zone) {
    let timeInZone;

    try {
        timeInZone = new Date(now.toLocaleString('en-US', { timeZone: zone }));
    } catch (e) {
        // TODO: indicate to the user that this is an invalid timezone
        console.error(e);
        timeInZone = now
    }

    const diffInMinutes = Math.round((timeInZone.getTime() - now.getTime()) / 1000 / 60);

    return { time: timeInZone, diffInMinutes: diffInMinutes };
}

function zoneDiffText(diffInMinutes) {
    if (diffInMinutes == 0) {
        return "";
    }

    const sign = diffInMinutes < 0 ? "-" : "+";
    const signText = diffInMinutes < 0 ? "behind" : "ahead";

    diffInMinutes = Math.abs(diffInMinutes);

    const hours = Math.floor(diffInMinutes / 60);
    const minutes = diffInMinutes % 60;
    const hourSuffix = hours == 1 ? "" : "s";

    if (minutes == 0) {
        return { text: `${sign}${hours}h`, title: `${hours} hour${hourSuffix} ${signText}` };
    }

    if (hours == 0) {
        return { text: `${sign}${minutes}m`, title: `${minutes} minutes ${signText}` };
    }

    return { text: `${sign}${hours}h~`, title: `${hours} hour${hourSuffix} and ${minutes} minutes ${signText}` };
}

function setupClocks() {
    const clocks = document.getElementsByClassName('clock');

    if (clocks.length == 0) {
        return;
    }

    const updateCallbacks = [];

    for (var i = 0; i < clocks.length; i++) {
        const clock = clocks[i];
        const hourFormat = clock.dataset.hourFormat;
        const localTimeContainer = clock.querySelector('[data-local-time]');
        const localDateElement = localTimeContainer.querySelector('[data-date]');
        const localWeekdayElement = localTimeContainer.querySelector('[data-weekday]');
        const localYearElement = localTimeContainer.querySelector('[data-year]');
        const timeZoneContainers = clock.querySelectorAll('[data-time-in-zone]');

        const setLocalTime = makeSettableTimeElement(
            localTimeContainer.querySelector('[data-time]'),
            hourFormat
        );

        updateCallbacks.push((now) => {
            setLocalTime(now);
            localDateElement.textContent = now.getDate() + ' ' + monthNames[now.getMonth()];
            localWeekdayElement.textContent = weekDayNames[now.getDay()];
            localYearElement.textContent = now.getFullYear();
        });

        for (var z = 0; z < timeZoneContainers.length; z++) {
            const timeZoneContainer = timeZoneContainers[z];
            const diffElement = timeZoneContainer.querySelector('[data-time-diff]');

            const setZoneTime = makeSettableTimeElement(
                timeZoneContainer.querySelector('[data-time]'),
                hourFormat
            );

            updateCallbacks.push((now) => {
                const { time, diffInMinutes } = timeInZone(now, timeZoneContainer.dataset.timeInZone);
                setZoneTime(time);
                const { text, title } = zoneDiffText(diffInMinutes);
                diffElement.textContent = text;
                diffElement.title = title;
            });
        }
    }

    const updateClocks = () => {
        const now = new Date();

        for (var i = 0; i < updateCallbacks.length; i++)
            updateCallbacks[i](now);

        setTimeout(updateClocks, (60 - now.getSeconds()) * 1000);
    };

    updateClocks();
}

async function setupCalendars() {
    const elems = document.getElementsByClassName("calendar");
    if (elems.length == 0) return;

    // TODO: implement prefetching, currently loads as a nasty waterfall of requests
    const calendar = await import ('./calendar.js');

    for (let i = 0; i < elems.length; i++)
        calendar.default(elems[i]);
}

async function setupTodos() {
    const elems = Array.from(document.getElementsByClassName("todo"));
    if (elems.length == 0) return;

    const todo = await import ('./todo.js');

    for (let i = 0; i < elems.length; i++){
        todo.default(elems[i]);
    }
}

function setupTruncatedElementTitles() {
    const elements = document.querySelectorAll(".text-truncate, .single-line-titles .title, .text-truncate-2-lines, .text-truncate-3-lines");

    if (elements.length == 0) {
        return;
    }

    for (let i = 0; i < elements.length; i++) {
        const element = elements[i];
        if (element.getAttribute("title") === null)
            element.title = element.innerText.trim().replace(/\s+/g, " ");
    }
}

async function changeTheme(key, onChanged) {
    const themeStyleElem = find("#theme-style");

    const response = await fetch(`${pageData.baseURL}/api/set-theme/${key}`, {
        method: "POST",
    });

    if (response.status != 200) {
        alert("Failed to set theme: " + response.statusText);
        return;
    }
    const newThemeStyle = await response.text();

    const tempStyle = elem("style")
        .html("* { transition: none !important; }")
        .appendTo(document.head);

    themeStyleElem.html(newThemeStyle);
    document.documentElement.setAttribute("data-theme", key);
    document.documentElement.setAttribute("data-scheme", response.headers.get("X-Scheme"));
    typeof onChanged == "function" && onChanged();
    setTimeout(() => { tempStyle.remove(); }, 10);
}

function initThemePicker() {
    const themeChoicesInMobileNav = find(".mobile-navigation .theme-choices");
    if (!themeChoicesInMobileNav) return;

    const themeChoicesInHeader = find(".header-container .theme-choices");

    if (themeChoicesInHeader) {
        themeChoicesInHeader.replaceWith(
            themeChoicesInMobileNav.cloneNode(true)
        );
    }

    const presetElems = findAll(".theme-choices .theme-preset");
    let themePreviewElems = document.getElementsByClassName("current-theme-preview");
    let isLoading = false;

    presetElems.forEach((presetElement) => {
        const themeKey = presetElement.dataset.key;

        if (themeKey === undefined) {
            return;
        }

        if (themeKey == pageData.theme) {
            presetElement.classList.add("current");
        }

        presetElement.addEventListener("click", () => {
            if (themeKey == pageData.theme) return;
            if (isLoading) return;

            isLoading = true;
            changeTheme(themeKey, function() {
                isLoading = false;
                pageData.theme = themeKey;
                presetElems.forEach((e) => { e.classList.remove("current"); });

                Array.from(themePreviewElems).forEach((preview) => {
                    preview.querySelector(".theme-preset").replaceWith(
                        presetElement.cloneNode(true)
                    );
                })

                presetElems.forEach((e) => {
                    if (e.dataset.key != themeKey) return;
                    e.classList.add("current");
                });
            });
        });
    })
}

async function setupPage() {
    initThemePicker();

    const pageElement = document.getElementById("page");
    const pageContentElement = document.getElementById("page-content");
    const loadingContainer = document.getElementById("page-loading-container");
    const loadingMessage = document.getElementById("page-loading-message");

    let pageContent = "";
    while (true) {
        const response = await fetchPageContent(pageData);

        if (!response.isCacheBuilding) {
            if (loadingContainer) {
                loadingContainer.classList.remove("cache-building");
            }
            pageContent = response.content;
            break;
        }

        if (loadingContainer) {
            loadingContainer.classList.add("cache-building");
        }
        if (loadingMessage) {
            loadingMessage.textContent = "Building cache...";
        }

        await new Promise(resolve => setTimeout(resolve, 1000));
    }

    pageContentElement.innerHTML = pageContent;

    try {
        setupPopovers();
        setupClocks()
        await setupCalendars();
        await setupTodos();
        setupCarousels();
        setupSearchBoxes();
        setupCollapsibleLists();
        setupCollapsibleGrids();
        setupGroups();
        setupMasonries();
        setupDynamicRelativeTime();
        setupLazyImages();
        setupPlayingProgressUpdater();
    } finally {
        pageElement.classList.add("content-ready");
        pageElement.setAttribute("aria-busy", "false");

        for (let i = 0; i < contentReadyCallbacks.length; i++) {
            contentReadyCallbacks[i]();
        }

        pageSetupComplete = true;
        _initSSE();

        setTimeout(() => {
            setupTruncatedElementTitles();
        }, 50);

        setTimeout(() => {
            document.body.classList.add("page-columns-transitioned");
        }, 300);
    }
}

async function fetchWidgetContent(widgetElement) {
    const widgetId = widgetElement.dataset.widgetId;
    if (!widgetId) {
        return null;
    }

    try {
        const response = await fetch(`${pageData.baseURL}/api/widgets/${widgetId}/content/`);
        if (!response.ok) {
            throw new Error(`Widget content request failed (${response.status})`);
        }

        const widgetHtml = await response.text();
        const tempDiv = document.createElement("div");
        tempDiv.innerHTML = widgetHtml;

        return tempDiv.querySelector(`.widget[data-widget-id="${widgetId}"]`);
    } catch (error) {
        console.error('Failed to fetch widget content:', error);
        return null;
    }
}

async function updateWidget(widgetElement) {
    setupUserScrollIntentTracking();

    const refreshStartedAt = nowMs();
    const widgetTopBefore = widgetElement.getBoundingClientRect().top;
    const collapsibleContainerStates = getCollapsibleContainerStates(widgetElement);
    const newWidget = await fetchWidgetContent(widgetElement);

    if (newWidget && widgetElement.outerHTML !== newWidget.outerHTML) {
        const oldContent = widgetElement.querySelector('.widget-content');
        const newContent = newWidget.querySelector('.widget-content');

        if (oldContent && newContent) {
            const savedInputs = {};
            for (const input of oldContent.querySelectorAll('input[id]')) {
                if (input.value) savedInputs[input.id] = input.value;
            }

            updateContentPreservingImages(oldContent, newContent);

            for (const [id, value] of Object.entries(savedInputs)) {
                const input = newContent.querySelector('#' + id);
                if (input) input.value = value;
            }

            const oldHeader = widgetElement.querySelector('.widget-header');
            const newHeader = newWidget.querySelector('.widget-header');
            if (oldHeader && newHeader && oldHeader.innerHTML !== newHeader.innerHTML) {
                oldHeader.innerHTML = newHeader.innerHTML;
            }

            const callbacksIndexBefore = contentReadyCallbacks.length;

            setupPopovers();
            setupCarousels();
            setupCollapsibleLists();
            setupCollapsibleGrids();
            setupGroups();
            setupMasonries();
            setupLazyImages();
            setupTruncatedElementTitles();
            setupDynamicRelativeTime();

            const newCallbacks = contentReadyCallbacks.splice(callbacksIndexBefore);
            for (const cb of newCallbacks) {
                cb();
            }

            restoreCollapsibleContainerStates(widgetElement, collapsibleContainerStates);
            setupPlayingProgressUpdater();
            setupPlayingThumbnailCropping();
            notifyWidgetUpdated(widgetElement);

            const widgetTopAfter = widgetElement.getBoundingClientRect().top;
            const topDelta = widgetTopAfter - widgetTopBefore;
            const userScrolledDuringRefresh = lastUserScrollIntentAt > refreshStartedAt;

            if (!userScrolledDuringRefresh && Math.abs(topDelta) > 1) {
                window.scrollBy({ top: topDelta, behavior: 'auto' });
            }
        }
    }
}

function updateContentPreservingImages(oldContent, newContent) {
    const oldImages = Array.from(oldContent.querySelectorAll('img[loading="lazy"]'));
    const newImages = Array.from(newContent.querySelectorAll('img[loading="lazy"]'));
    const imageMap = new Map();

    for (const img of oldImages) {
        const list = imageMap.get(img.src);
        if (list) {
            list.push(img);
        } else {
            imageMap.set(img.src, [img]);
        }
    }

    for (const newImg of newImages) {
        const list = imageMap.get(newImg.src);
        if (list && list.length > 0) {
            const oldImg = list.shift();
            for (const attr of newImg.attributes) {
                if (attr.name !== 'src' && attr.name !== 'class') {
                    oldImg.setAttribute(attr.name, attr.value);
                }
            }
            newImg.replaceWith(oldImg);
        }
    }

    oldContent.replaceWith(newContent);
}

function notifyWidgetUpdated(widgetElement) {
    widgetElement.dispatchEvent(new CustomEvent('dynacat:widget-updated', {
        bubbles: true,
        detail: {
            widget: widgetElement,
            widgetId: widgetElement.dataset.widgetId || null,
        },
    }));
}

function nowMs() {
    return Date.now();
}

let userScrollIntentTrackingInitialized = false;
let lastUserScrollIntentAt = 0;

function setupUserScrollIntentTracking() {
    if (userScrollIntentTrackingInitialized) {
        return;
    }

    const markUserScrollIntent = (event) => {
        if (event.type === "keydown") {
            const key = event.key;
            if (key !== "ArrowUp" && key !== "ArrowDown" && key !== "PageUp" && key !== "PageDown" && key !== "Home" && key !== "End" && key !== " ") {
                return;
            }
        }

        lastUserScrollIntentAt = nowMs();
    };

    window.addEventListener("wheel", markUserScrollIntent, { passive: true });
    window.addEventListener("touchmove", markUserScrollIntent, { passive: true });
    window.addEventListener("keydown", markUserScrollIntent, { passive: true });

    userScrollIntentTrackingInitialized = true;
}

function remainingDelayMs(intervalMs, lastRunAt) {
    if (lastRunAt == null) {
        return intervalMs;
    }

    const elapsed = nowMs() - lastRunAt;
    return elapsed >= intervalMs ? 0 : intervalMs - elapsed;
}

const widgetPollingStates = new Map();
let widgetPollingVisibilityListenerInitialized = false;

// Local playing progress updaters keyed by widget element
const playingUpdaters = new Map();

function clearPlayingUpdater(widget) {
    const state = playingUpdaters.get(widget);
    if (!state) return;
    if (state.intervalId != null) {
        clearInterval(state.intervalId);
    }
    playingUpdaters.delete(widget);
}

function formatDurationMs(ms) {
    ms = Math.max(0, Math.floor(ms));
    const totalSeconds = Math.floor(ms / 1000);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;

    if (hours >= 1) {
        return `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
    }

    return `${minutes}:${String(seconds).padStart(2, '0')}`;
}

const playingSessionStates = new Map();

function _playingEstimate(state) {
    if (!state.isPlaying) return state.anchorOffset;
    return state.anchorOffset + (Date.now() - state.anchorTime);
}

function _playingRender(sess, offsetMs, durationMs) {
    const pct = durationMs > 0 ? Math.min(100, (offsetMs / durationMs) * 100) : 0;
    const fill = sess.querySelector('.playing-progress-fill');
    if (fill) fill.style.width = pct + '%';
    sess.querySelectorAll('.playing-time-pos').forEach(el => {
        el.textContent = formatDurationMs(offsetMs);
    });
}

function _playingSyncWidget(widget) {
    const DRIFT_TOLERANCE_MS = 3000;
    const sessions = widget.querySelectorAll('.playing-session[data-duration][data-offset]');
    sessions.forEach((sess, idx) => {
        const duration = Number(sess.dataset.duration || 0);
        if (!duration) return;

        const serverOffset = Number(sess.dataset.offset || 0);
        const isPlaying = sess.dataset.playing === 'true';
        const key = sess.dataset.sessionKey || (widget.dataset.widgetId + ':' + idx);

        let state = playingSessionStates.get(key);
        if (state) {
            const estimated = _playingEstimate(state);
            const drift = Math.abs(serverOffset - estimated);
            const stateChanged = state.isPlaying !== isPlaying;
            if (stateChanged || drift > DRIFT_TOLERANCE_MS) {
                state.anchorOffset = serverOffset;
                state.anchorTime = Date.now();
                state.isPlaying = isPlaying;
            }
        } else {
            state = { anchorOffset: serverOffset, anchorTime: Date.now(), isPlaying };
            playingSessionStates.set(key, state);
        }

        _playingRender(sess, Math.min(_playingEstimate(state), duration), duration);
    });
}

function _playingTickWidget(widget) {
    const sessions = widget.querySelectorAll('.playing-session[data-duration][data-offset]');
    sessions.forEach((sess, idx) => {
        const duration = Number(sess.dataset.duration || 0);
        if (!duration) return;
        const key = sess.dataset.sessionKey || (widget.dataset.widgetId + ':' + idx);
        const state = playingSessionStates.get(key);
        if (!state) return;
        _playingRender(sess, Math.min(_playingEstimate(state), duration), duration);
    });
}

function setupPlayingProgressUpdater() {
    const widgets = document.querySelectorAll('.widget[data-update-interval]');
    const seen = new Set();

    widgets.forEach(widget => {
        seen.add(widget);
        _playingSyncWidget(widget);

        if (playingUpdaters.has(widget)) return;

        const intervalId = setInterval(() => _playingTickWidget(widget), 1000);
        playingUpdaters.set(widget, { intervalId });
    });

    Array.from(playingUpdaters.keys()).forEach(widget => {
        if (!seen.has(widget)) clearPlayingUpdater(widget);
    });

    setupPlayingThumbnailCropping();
}

function setupPlayingThumbnailCropping() {
    const imgs = document.querySelectorAll('.playing-thumbnail-img');

    imgs.forEach(img => {
        const container = img.closest('.playing-thumbnail');
        const apply = () => {
            if (!img.naturalWidth || !img.naturalHeight) {
                img.classList.remove('playing-crop');
                if (container) container.classList.remove('playing-portrait');
                return;
            }

            if (img.naturalWidth > img.naturalHeight) {
                img.classList.add('playing-crop');
                if (container) container.classList.remove('playing-portrait');
            } else if (img.naturalHeight > img.naturalWidth) {
                img.classList.remove('playing-crop');
                if (container) container.classList.add('playing-portrait');
            } else {
                img.classList.remove('playing-crop');
                if (container) container.classList.remove('playing-portrait');
            }
        };

        if (img.complete) {
            apply();
        } else {
            img.addEventListener('load', apply, { once: true });
            img.addEventListener('error', () => {
                img.classList.remove('playing-crop');
                if (container) container.classList.remove('playing-portrait');
            }, { once: true });
        }
    });
}

function clearWidgetPollingTimeout(state) {
    if (state.timeoutId != null) {
        clearTimeout(state.timeoutId);
        state.timeoutId = null;
    }
}

function clearWidgetPollingState(widget) {
    const state = widgetPollingStates.get(widget);
    if (!state) {
        return;
    }

    clearWidgetPollingTimeout(state);
    widgetPollingStates.delete(widget);
}

function scheduleWidgetPolling(state, delayMs) {
    clearWidgetPollingTimeout(state);
    state.timeoutId = setTimeout(() => {
        pollWidget(state);
    }, Math.max(0, delayMs));
}

async function pollWidget(state) {
    if (state.isFetching) {
        return;
    }

    const widget = state.widget;

    if (!widget.isConnected) {
        clearWidgetPollingState(widget);
        return;
    }

    if (document.hidden) {
        return;
    }

    state.isFetching = true;
    try {
        await updateWidget(widget);
        state.lastRunAt = nowMs();
    } finally {
        state.isFetching = false;

        if (!document.hidden && widget.isConnected) {
            scheduleWidgetPolling(state, state.intervalMs);
        }
    }
}

function registerWidgetPolling(widget, intervalMs) {
    let state = widgetPollingStates.get(widget);

    if (!state) {
        state = {
            widget,
            intervalMs,
            timeoutId: null,
            isFetching: false,
            lastRunAt: nowMs(),
        };
        widgetPollingStates.set(widget, state);
    } else {
        state.intervalMs = intervalMs;
    }

    if (!document.hidden) {
        scheduleWidgetPolling(state, remainingDelayMs(state.intervalMs, state.lastRunAt));
    }
}

function handleWidgetPollingVisibilityChange() {
    if (document.hidden) {
        widgetPollingStates.forEach((state) => {
            clearWidgetPollingTimeout(state);
        });
        return;
    }

    widgetPollingStates.forEach((state, widget) => {
        if (!widget.isConnected) {
            clearWidgetPollingState(widget);
            return;
        }

        scheduleWidgetPolling(state, remainingDelayMs(state.intervalMs, state.lastRunAt));
    });
}

function setupWidgetPolling() {
    const pollingWidgets = document.querySelectorAll('.widget[data-update-interval]');
    const seenWidgets = new Set();

    pollingWidgets.forEach(widget => {
        const intervalMs = parseInt(widget.dataset.updateInterval, 10);

        if (isNaN(intervalMs) || intervalMs <= 0) {
            console.error('Invalid update-interval for widget:', widget.dataset.updateInterval);
            return;
        }

        seenWidgets.add(widget);
        registerWidgetPolling(widget, intervalMs);
    });

    widgetPollingStates.forEach((state, widget) => {
        if (seenWidgets.has(widget)) {
            return;
        }

        clearWidgetPollingState(widget);
    });

    if (!widgetPollingVisibilityListenerInitialized) {
        document.addEventListener("visibilitychange", handleWidgetPollingVisibilityChange);
        widgetPollingVisibilityListenerInitialized = true;
    }
}

async function applyContentUpdate() {
    setupUserScrollIntentTracking();

    const refreshStartedAt = nowMs();
    const scrollThreshold = 100;
    const wasAtBottom = (window.innerHeight + window.scrollY) >= (document.documentElement.scrollHeight - scrollThreshold);
    const anchorWidget = Array.from(document.querySelectorAll('.widget')).find(widget => {
        const rect = widget.getBoundingClientRect();
        return rect.bottom > 0 && rect.top < window.innerHeight;
    }) || null;
    const anchorTopBefore = anchorWidget ? anchorWidget.getBoundingClientRect().top : null;
    const response = await fetchPageContent(pageData);
    const pageContent = response.content;
    const tempDiv = document.createElement("div");
    tempDiv.innerHTML = pageContent;

    const realContainers = Array.from(document.querySelectorAll(".head-widgets, .page-column"));
    const tempContainers = Array.from(tempDiv.querySelectorAll(".head-widgets, .page-column"));

    let anyReplaced = false;
    const collapsibleStatesMap = new Map();
    const updatedWidgets = [];

    for (let i = 0; i < Math.min(realContainers.length, tempContainers.length); i++) {
        const realWidgets = Array.from(realContainers[i].children);
        const tempWidgets = Array.from(tempContainers[i].children);

        for (let j = 0; j < Math.min(realWidgets.length, tempWidgets.length); j++) {
            const realWidget = realWidgets[j];
            const tempWidget = tempWidgets[j];

            collapsibleStatesMap.set(realWidget, getCollapsibleContainerStates(realWidget));

            if (realWidget.dataset.updateInterval && realWidget.outerHTML !== tempWidget.outerHTML) {
                const oldContent = realWidget.querySelector('.widget-content');
                const newContent = tempWidget.querySelector('.widget-content');

                if (oldContent && newContent) {
                    updateContentPreservingImages(oldContent, newContent);

                    const oldHeader = realWidget.querySelector('.widget-header');
                    const newHeader = tempWidget.querySelector('.widget-header');
                    if (oldHeader && newHeader && oldHeader.innerHTML !== newHeader.innerHTML) {
                        oldHeader.innerHTML = newHeader.innerHTML;
                    }

                    anyReplaced = true;
                    updatedWidgets.push(realWidget);
                }
            }
        }
    }

    if (anyReplaced) {
        const callbacksIndexBefore = contentReadyCallbacks.length;

        setupPopovers();
        setupCarousels();
        setupCollapsibleLists();
        setupCollapsibleGrids();
        setupGroups();
        setupMasonries();
        setupLazyImages();
        setupTruncatedElementTitles();
        setupPlayingThumbnailCropping();

        const newCallbacks = contentReadyCallbacks.splice(callbacksIndexBefore);
        for (const cb of newCallbacks) {
            cb();
        }

        for (const [widget, states] of collapsibleStatesMap) {
            restoreCollapsibleContainerStates(widget, states);
        }

        setupPlayingProgressUpdater();
        for (const widget of updatedWidgets) {
            notifyWidgetUpdated(widget);
        }

        const userScrolledDuringRefresh = lastUserScrollIntentAt > refreshStartedAt;

        if (!userScrolledDuringRefresh) {
            if (wasAtBottom) {
                const maxScroll = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
                if (Math.abs(window.scrollY - maxScroll) > 1) {
                    window.scrollTo({ top: maxScroll, behavior: 'auto' });
                }
            } else if (anchorWidget && anchorTopBefore != null) {
                const anchorTopAfter = anchorWidget.getBoundingClientRect().top;
                const topDelta = anchorTopAfter - anchorTopBefore;
                if (Math.abs(topDelta) > 1) {
                    window.scrollBy({ top: topDelta, behavior: 'auto' });
                }
            }
        }
    }
}

let pollingTimeout = null;
let isPollingFetchInProgress = false;
let pageLastPollAt = null;
let pagePollingVisibilityListenerInitialized = false;

function clearPagePollingTimeout() {
    if (pollingTimeout != null) {
        clearTimeout(pollingTimeout);
        pollingTimeout = null;
    }
}

function schedulePagePoll(poll, delayMs) {
    clearPagePollingTimeout();
    pollingTimeout = setTimeout(poll, Math.max(0, delayMs));
}

function startPolling() {
    if (!pageData.updateInterval || pageData.updateInterval <= 0) return;

    const poll = async () => {
        if (isPollingFetchInProgress) return;

        clearPagePollingTimeout();

        if (document.hidden) {
            return;
        }

        isPollingFetchInProgress = true;
        try {
            await applyContentUpdate();
            pageLastPollAt = nowMs();
        } finally {
            isPollingFetchInProgress = false;
            if (!document.hidden) {
                schedulePagePoll(poll, pageData.updateInterval);
            }
        }
    };

    const handlePagePollingVisibilityChange = () => {
        if (document.hidden) {
            clearPagePollingTimeout();
        } else {
            if (pageLastPollAt == null) {
                poll();
                return;
            }

            schedulePagePoll(poll, remainingDelayMs(pageData.updateInterval, pageLastPollAt));
        }
    };

    if (!pagePollingVisibilityListenerInitialized) {
        document.addEventListener("visibilitychange", handlePagePollingVisibilityChange);
        pagePollingVisibilityListenerInitialized = true;
    }

    poll();
}

window.dynacatRefreshWidget = async function(widgetId) {
    const widget = document.querySelector(`.widget[data-widget-id="${widgetId}"]`);
    if (widget) {
        await updateWidget(widget);
        return;
    }

    _dynacatFetchAndApplyWidget(widgetId);
};

function _dynacatFetchAndApplyWidget(widgetId) {
    const url = `${pageData.baseURL}/api/widgets/${widgetId}/content/`;
    fetch(url, { credentials: 'include' })
        .then(r => r.ok ? r.text() : Promise.reject(r.status))
        .then(html => _applyWidgetUpdate(widgetId, html))
        .catch(err => console.error('Widget refresh failed', widgetId, err));
}

function _applyWidgetUpdate(widgetId, html) {
    const target = document.querySelector(`.widget[data-widget-id="${widgetId}"]`);
    if (!target) return;

    const collapsibleContainerStates = getCollapsibleContainerStates(target);
    const htmlElem = document.documentElement;
    const prevAnchor = htmlElem.style.overflowAnchor;
    htmlElem.style.overflowAnchor = 'none';

    try {
        Idiomorph.morph(target, html, { morphStyle: 'outerHTML' });

        const liveTarget = document.querySelector(`.widget[data-widget-id="${widgetId}"]`);
        if (!liveTarget) return;

        setupCollapsibleLists();
        setupCollapsibleGrids();

        if (collapsibleContainerStates?.length) {
            restoreCollapsibleContainerStates(liveTarget, collapsibleContainerStates);
        }

        const lazyImages = liveTarget.querySelectorAll('img[loading=lazy]');
        for (let i = 0; i < lazyImages.length; i++) {
            const img = lazyImages[i];
            if (img.complete && img.naturalHeight > 0) {
                img.classList.add('cached', 'finished-transition');
                img.dataset.lazyInitialized = 'true';
            }
        }

        const groupContents = liveTarget.querySelectorAll('.widget-group-content');
        for (let i = 0; i < groupContents.length; i++) {
            groupContents[i].style.animation = 'none';
        }

        _runPostSettleSetup();
        notifyWidgetUpdated(liveTarget);

    } finally {
        htmlElem.style.overflowAnchor = prevAnchor;
    }
}

function _runPostSettleSetup() {
    setupPopovers();
    setupCarousels();
    setupGroups();
    setupMasonries();
    setupDynamicRelativeTime();
    setupLazyImages();
    setupTruncatedElementTitles();
    setupPlayingProgressUpdater();
    setupPlayingThumbnailCropping();
}

let _sseSource = null;
let _intentionallyClosed = false;

function _closeSSE() {
    if (_sseSource) {
        _intentionallyClosed = true;
        _sseSource.close();
        _sseSource = null;
    }
}

function _initSSE() {
    if (!pageData.dynamicUpdateEnabled) {
        return;
    }

    const url = `${pageData.baseURL}/api/sse/updates`;
    _sseSource = new EventSource(url, { withCredentials: true });

    _sseSource.addEventListener('widget-update', function(event) {
        try {
            const data = JSON.parse(event.data);
            _applyWidgetUpdate(data.widgetId, data.html);
        } catch (e) {
            console.error('SSE parse error', e);
        }
    });

    _sseSource.onerror = function() {
        if (_intentionallyClosed) {
            return;
        }
        if (_sseSource.readyState === EventSource.CLOSED) {
            window.location.reload();
        }
    };
}

window.addEventListener('beforeunload', _closeSSE);

window.dynacatSetupPopovers = setupPopovers;

setupPage().then(() => {
    startPolling();
    setupWidgetPolling();
});
