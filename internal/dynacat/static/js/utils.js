export function throttledDebounce(callback, maxDebounceTimes, debounceDelay) {
    let debounceTimeout;
    let timesDebounced = 0;

    return function () {
        if (timesDebounced == maxDebounceTimes) {
            clearTimeout(debounceTimeout);
            timesDebounced = 0;
            callback();
            return;
        }

        clearTimeout(debounceTimeout);
        timesDebounced++;

        debounceTimeout = setTimeout(() => {
            timesDebounced = 0;
            callback();
        }, debounceDelay);
    };
};

export function isElementVisible(element) {
    return !!(element.offsetWidth || element.offsetHeight || element.getClientRects().length);
}

export function clamp(value, min, max) {
    return Math.min(Math.max(value, min), max);
}

// Relative URLs resolve against the page and app schemes like steam: stay allowed, so only script-capable schemes are rejected.
export function isNavigableURL(url) {
    try {
        return !["javascript:", "data:", "vbscript:"].includes(new URL(url, location.href).protocol);
    } catch {
        return false;
    }
}

export function openURLInNewTab(url, focus = true) {
    if (!isNavigableURL(url)) return;
    const newWindow = window.open(url, '_blank', 'noopener,noreferrer');

    if (focus && newWindow != null) newWindow.focus();
}


export class Vec2 {
    constructor(x, y) {
        this.x = x;
        this.y = y;
    }

    static new(x = 0, y = 0) {
        return new Vec2(x, y);
    }

    static fromEvent(event) {
        return new Vec2(event.clientX, event.clientY);
    }

    setFromEvent(event) {
        this.x = event.clientX;
        this.y = event.clientY;
        return this;
    }

    set(x, y) {
        this.x = x;
        this.y = y;
        return this;
    }
}

export function toggleableEvents(element, eventToHandlerMap) {
    return [
        () => {
            for (const [event, handler] of Object.entries(eventToHandlerMap)) {
                element.addEventListener(event, handler);
            }
        },
        () => {
            for (const [event, handler] of Object.entries(eventToHandlerMap)) {
                element.removeEventListener(event, handler);
            }
        }
    ];
}
