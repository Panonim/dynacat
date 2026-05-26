const pendingMobileSectionKey = "dynacat:pending-mobile-section";
const minSwipeDistance = 30;
const intentDistance = 8;
const horizontalRatio = 1.1;
const clickSuppressMs = 500;
const scrollSlack = 1;
const ignoredTargetSelector = ".mobile-navigation, input, textarea, select, button, label, summary, [contenteditable=true]";

let mobileSwipesInitialized = false;
let gesture = null;
let suppressClickUntil = 0;

export function setupMobileSwipes() {
    if (mobileSwipesInitialized) {
        return;
    }

    mobileSwipesInitialized = true;
    restorePendingMobileSection();

    document.addEventListener("click", handleClick, true);
    document.addEventListener("touchstart", handleTouchStart, { passive: true });
    document.addEventListener("touchmove", handleTouchMove, { passive: false });
    document.addEventListener("touchend", handleTouchEnd, { passive: false });
    document.addEventListener("touchcancel", resetGesture, { passive: true });
}

function handleClick(event) {
    if (Date.now() > suppressClickUntil) {
        return;
    }

    suppressClickUntil = 0;
    preventDefault(event);
    event.stopPropagation();
}

function handleTouchStart(event) {
    const touch = getSingleTouch(event.touches);

    if (!isMobileSwipeMode() || !touch || shouldIgnoreTarget(event.target)) {
        resetGesture();
        return;
    }

    gesture = {
        startX: touch.clientX,
        startY: touch.clientY,
        horizontal: false,
        scrollContainer: getHorizontalScrollContainer(event.target),
    };
}

function handleTouchMove(event) {
    const touch = getSingleTouch(event.touches);

    if (!gesture || !isMobileSwipeMode() || !touch) {
        resetGesture();
        return;
    }

    const swipe = getSwipe(gesture, touch);

    if (!gesture.horizontal && swipe.absY > intentDistance && swipe.absY > swipe.absX) {
        resetGesture();
        return;
    }

    if (!gesture.horizontal && !isHorizontalSwipe(swipe, intentDistance)) {
        return;
    }

    if (!gesture.horizontal && canScrollHorizontally(gesture.scrollContainer, swipe.deltaX)) {
        resetGesture();
        return;
    }

    gesture.horizontal = true;
    preventDefault(event);
}

function handleTouchEnd(event) {
    if (!gesture || !isMobileSwipeMode() || event.changedTouches.length == 0) {
        resetGesture();
        return;
    }

    const swipe = getSwipe(gesture, event.changedTouches[0]);

    if (isHorizontalSwipe(swipe, minSwipeDistance)) {
        const activated = activateSwipe(swipe.deltaX < 0 ? 1 : -1);

        if (activated) {
            suppressClickUntil = Date.now() + clickSuppressMs;
            preventDefault(event);
        }
    }

    resetGesture();
}

function getSingleTouch(touches) {
    return touches.length == 1 ? touches[0] : null;
}

function getSwipe(gesture, touch) {
    const deltaX = touch.clientX - gesture.startX;
    const deltaY = touch.clientY - gesture.startY;

    return {
        deltaX,
        absX: Math.abs(deltaX),
        absY: Math.abs(deltaY),
    };
}

function isHorizontalSwipe(swipe, minDistance) {
    return swipe.absX >= minDistance && swipe.absX > swipe.absY * horizontalRatio;
}

function resetGesture() {
    gesture = null;
}

function preventDefault(event) {
    if (event.cancelable) {
        event.preventDefault();
    }
}

function shouldIgnoreTarget(target) {
    return !(target instanceof Element) || target.closest(ignoredTargetSelector) !== null;
}

function isMobileSwipeMode() {
    const nav = document.querySelector(".mobile-navigation");
    return nav !== null && window.getComputedStyle(nav).display !== "none";
}

function getHorizontalScrollContainer(target) {
    if (!(target instanceof Element)) {
        return null;
    }

    for (let element = target; element && element !== document.body; element = element.parentElement) {
        const style = window.getComputedStyle(element);
        if ((style.overflowX == "auto" || style.overflowX == "scroll") && element.scrollWidth > element.clientWidth) {
            return element;
        }
    }

    return null;
}

function canScrollHorizontally(element, deltaX) {
    if (!element) {
        return false;
    }

    const maxScrollLeft = element.scrollWidth - element.clientWidth;

    if (deltaX < 0) {
        return element.scrollLeft < maxScrollLeft - scrollSlack;
    }

    return element.scrollLeft > scrollSlack;
}

function activateSwipe(direction) {
    return activateMobileSection(direction) || activateMobilePage(direction);
}

function activateMobileSection(direction) {
    const inputs = getMobileSectionInputs();
    const current = inputs.findIndex(input => input.checked);
    if (current < 0) {
        return false;
    }

    const next = current + direction;

    if (!inputs[next]) {
        return false;
    }

    inputs[next].click();
    return true;
}

function activateMobilePage(direction) {
    const links = [...document.querySelectorAll(".mobile-navigation-page-links .nav-item")];

    if (links.length < 2) {
        return false;
    }

    const current = links.findIndex(isCurrentPageLink);
    if (current < 0) {
        return false;
    }

    const next = (current + direction + links.length) % links.length;

    setPendingMobileSection(direction < 0 ? "end" : "start");
    links[next].click();
    return true;
}

function isCurrentPageLink(link) {
    return link.getAttribute("aria-current") == "page" || link.classList.contains("nav-item-current") || link.href == window.location.href;
}

function restorePendingMobileSection() {
    const pending = takePendingMobileSection();

    if (pending !== "start" && pending !== "end") {
        return;
    }

    const inputs = getMobileSectionInputs();
    const input = pending == "end" ? inputs[inputs.length - 1] : inputs[0];

    if (input && !input.checked) {
        input.click();
    }
}

function takePendingMobileSection() {
    try {
        const pending = sessionStorage.getItem(pendingMobileSectionKey);
        sessionStorage.removeItem(pendingMobileSectionKey);
        return pending;
    } catch (e) {
        return null;
    }
}

function setPendingMobileSection(section) {
    try {
        sessionStorage.setItem(pendingMobileSectionKey, section);
    } catch (e) {
        return;
    }
}

function getMobileSectionInputs() {
    return [...document.querySelectorAll(".mobile-navigation-input")];
}
