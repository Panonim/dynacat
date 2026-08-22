import { elem, fragment } from "./templating.js";
import { animateReposition } from "./animations.js";
import { clamp, Vec2, toggleableEvents, throttledDebounce } from "./utils.js";

const trashIconSvg = `<svg fill="currentColor" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">
  <path fill-rule="evenodd" d="M5 3.25V4H2.75a.75.75 0 0 0 0 1.5h.3l.815 8.15A1.5 1.5 0 0 0 5.357 15h5.285a1.5 1.5 0 0 0 1.493-1.35l.815-8.15h.3a.75.75 0 0 0 0-1.5H11v-.75A2.25 2.25 0 0 0 8.75 1h-1.5A2.25 2.25 0 0 0 5 3.25Zm2.25-.75a.75.75 0 0 0-.75.75V4h3v-.75a.75.75 0 0 0-.75-.75h-1.5ZM6.05 6a.75.75 0 0 1 .787.713l.275 5.5a.75.75 0 0 1-1.498.075l-.275-5.5A.75.75 0 0 1 6.05 6Zm3.9 0a.75.75 0 0 1 .712.787l-.275 5.5a.75.75 0 0 1-1.498-.075l.275-5.5a.75.75 0 0 1 .786-.711Z" clip-rule="evenodd" />
</svg>`;
const dragIconSvg = `<svg fill="currentColor" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">
  <path d="M6.5 3.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0ZM6.5 8a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0ZM5.25 14.75a1.25 1.25 0 1 0 0-2.5 1.25 1.25 0 0 0 0 2.5ZM12 3.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0ZM10.75 9.25a1.25 1.25 0 1 0 0-2.5 1.25 1.25 0 0 0 0 2.5ZM12 13.5a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Z"/>
</svg>`;

export default function(element) {
    element.swapWith(
        Todo(
            element.dataset.todoId,
            element.dataset.todoStorage,
            element.dataset.todoCollapseAfter
        )
    )
}

function itemAnim(height, entrance = true) {
    const visible = { height: height + "px", opacity: 1 };
    const hidden  = { height: "0", opacity: 0, padding: "0" };

    return {
        keyframes: [
            entrance ? hidden : visible,
            entrance ? visible : hidden
        ],
        options: { duration: 200, easing: "ease" }
    }
}

function inputMarginAnim(entrance = true) {
    const amount = "1.5rem";

    return {
        keyframes: [
            { marginBottom: entrance ? "0px" : amount },
            { marginBottom: entrance ? amount : "0" }
        ],
        options: { duration: 200, easing: "ease", fill: "forwards" }
    }
}

function loadFromLocalStorage(id) {
    return JSON.parse(localStorage.getItem(`todo-${id}`) || "[]");
}

function saveToLocalStorage(id, data) {
    localStorage.setItem(`todo-${id}`, JSON.stringify(data));
}

async function loadFromServer(id) {
    const res = await fetch(`/api/todo/${encodeURIComponent(id)}`);
    return res.ok ? res.json() : [];
}

async function saveToServer(id, data) {
    await fetch(`/api/todo/${encodeURIComponent(id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data)
    });
}

function Item(unserialize = {}, onTextUpdate, onCheckUpdate, onDelete, onEscape, onDragStart) {
    let item, input, inputArea;

    const serializeable = {
        text: unserialize.text || "",
        checked: unserialize.checked || false
    };

    item = elem().classes("todo-item", "flex", "gap-10", "items-center").append(
        elem("input")
            .classes("todo-item-checkbox", "shrink-0")
            .styles({ marginTop: "-0.1rem" })
            .attrs({ type: "checkbox" })
            .on("change", (e) => {
                serializeable.checked = e.target.checked;
                onCheckUpdate();
            })
            .tap(self => self.checked = serializeable.checked),

        input = autoScalingTextarea(textarea => inputArea = textarea
            .classes("todo-item-text")
            .attrs({
                placeholder: "empty task",
                spellcheck: "false"
            })
            .on("keydown", (e) => {
                if (e.key === "Enter") {
                    e.preventDefault();
                } else if (e.key === "Escape") {
                    e.preventDefault();
                    onEscape();
                }
            })
            .on("input", () => {
                serializeable.text = inputArea.value;
                onTextUpdate();
            })
        ).classes("min-width-0", "grow"),

        elem("button")
            .classes("todo-item-drag-handle", "shrink-0")
            .attrs({
                type: "button",
                "aria-label": "Reorder task"
            })
            .html(dragIconSvg)
            .on("pointerdown", (e) => onDragStart(e, item)),

        elem("button")
            .classes("todo-item-delete", "shrink-0")
            .attrs({
                type: "button",
                "aria-label": "Delete task"
            })
            .html(trashIconSvg)
            .on("click", () => onDelete(item))
    );

    item.on("pointerdown", (event) => {
        if (event.pointerType !== "mouse") return;
        if (event.button !== 0) return;

        const startTarget = event.target;
        if (!(startTarget instanceof HTMLElement)) return;
        if (startTarget.closest(".todo-item-delete, .todo-item-checkbox")) return;

        const startX = event.clientX;
        const startY = event.clientY;
        const pointerId = event.pointerId;
        const dragThreshold = 4;

        const cleanup = () => {
            document.removeEventListener("pointermove", handlePointerMove);
            document.removeEventListener("pointerup", handlePointerEnd);
            document.removeEventListener("pointercancel", handlePointerEnd);
        };

        const handlePointerEnd = () => {
            cleanup();
        };

        const handlePointerMove = (moveEvent) => {
            if (moveEvent.pointerId !== pointerId) return;

            const distance = Math.hypot(
                moveEvent.clientX - startX,
                moveEvent.clientY - startY
            );

            if (distance > 0) {
                moveEvent.preventDefault();
            }

            if (distance < dragThreshold) return;

            cleanup();
            onDragStart(moveEvent, item);
        };

        document.addEventListener("pointermove", handlePointerMove);
        document.addEventListener("pointerup", handlePointerEnd);
        document.addEventListener("pointercancel", handlePointerEnd);
    });

    input.component.setValue(serializeable.text);
    return item.component({
        focusInput: () => inputArea.focus(),
        serialize: () => serializeable
    });
}

function Todo(id, storageType, collapseAfterConfig) {
    const useServer = storageType === "server";
    const parsedCollapseAfter = collapseAfterConfig === undefined
        ? NaN
        : parseInt(collapseAfterConfig, 10);
    const shouldCollapse = Number.isInteger(parsedCollapseAfter) && parsedCollapseAfter >= 0;
    const collapseAfter = shouldCollapse ? parsedCollapseAfter : 0;
    let items, input, inputArea, inputContainer, lastAddedItem;
    let expandButton, expandButtonTextNode;
    let queuedForRemoval = 0;
    let reorderable;
    let isDragging = false;
    let isExpanded = false;

    const applyCollapsibleState = () => {
        if (!shouldCollapse) {
            return;
        }

        const shouldShowButton = items.children.length > collapseAfter;
        if (shouldShowButton) {
            if (!expandButton.isConnected) {
                items.after(expandButton);
            }
        } else if (expandButton.isConnected) {
            expandButton.remove();
        }

        if (!shouldShowButton) {
            isExpanded = false;
            items.classList.remove("container-expanded");
            expandButton.classList.remove("container-expanded");
            expandButtonTextNode.nodeValue = "Show more";
        }

        for (let i = 0; i < items.children.length; i++) {
            const child = items.children[i];
            const isCollapsibleItem = i >= collapseAfter;
            const wasCollapsibleItem = child.classList.contains("collapsible-item");

            child.classesIf(isCollapsibleItem, "collapsible-item");

            if (isCollapsibleItem && !wasCollapsibleItem) {
                child.style.animationDelay = ((i - collapseAfter) * 20).toString() + "ms";
            } else if (!isCollapsibleItem) {
                child.style.removeProperty("animation-delay");
            }
        }
    };

    const onDragEnd = () => {
        isDragging = false;
        if (shouldCollapse) items.classList.add("no-reveal-animation");
        applyCollapsibleState();
    };
    const onDragStart = (event, element) => {
        isDragging = true;
        reorderable.component.onDragStart(event, element);
    };

    const serializeItems = () => items.children.map(item => item.component.serialize());
    const saveItems = () => {
        if (isDragging) return;

        const data = serializeItems();
        if (useServer) {
            saveToServer(id, data);
        } else {
            saveToLocalStorage(id, data);
        }
    };
    const renderItems = (data) => {
        items.replaceChildren(...data.map(d => newItem(d)));
        inputContainer.classesIf(data.length > 0, "margin-bottom-15");
        applyCollapsibleState();
    };

    const onItemRepositioned = () => saveItems();
    const debouncedOnItemUpdate = throttledDebounce(saveItems, 10, 1000);

    const onItemDelete = (item) => {
        if (lastAddedItem === item) lastAddedItem = null;
        const height = item.clientHeight;
        queuedForRemoval++;
        item.animate(itemAnim(height, false), () => {
            item.remove();
            queuedForRemoval--;
            saveItems();
            applyCollapsibleState();
        });

        if (items.children.length - queuedForRemoval === 0)
            inputContainer.animate(inputMarginAnim(false));
    };

    const newItem = (data) => Item(
        data,
        debouncedOnItemUpdate,
        saveItems,
        onItemDelete,
        () => inputArea.focus(),
        onDragStart
    );

    const addNewItem = (itemText, prepend) => {
        const totalItemsBeforeAppending = items.children.length;
        const item = lastAddedItem = newItem({ text: itemText });

        prepend ? items.prepend(item) : items.append(item);
        saveItems();
        const height = item.clientHeight;
        item.animate(itemAnim(height));
        applyCollapsibleState();

        if (totalItemsBeforeAppending === 0)
            inputContainer.animate(inputMarginAnim());
    };

    const handleInputKeyDown = (e) => {
        switch (e.key) {
            case "Enter":
                e.preventDefault();
                submitInputValue(e.ctrlKey);
                break;
            case "Escape":
                e.target.blur();
                break;
            case "ArrowDown":
                if (!lastAddedItem) return;
                e.preventDefault();
                lastAddedItem.component.focusInput();
                break;
        }
    };
    const submitInputValue = (prepend = false) => {
        const value = inputArea.value.trim();
        if (value === "") return;
        addNewItem(value, prepend);
        input.component.setValue("");
        inputArea.focus();
    };

    const initialData = useServer ? [] : loadFromLocalStorage(id);

    items = elem()
        .classes("todo-items")
        .classesIf(shouldCollapse, "collapsible-container")
        .append(
            ...initialData.map(data => newItem(data))
        );

    if (shouldCollapse) {
        expandButtonTextNode = document.createTextNode("Show more");
        expandButton = elem("button")
            .classes("expand-toggle-button")
            .append(
                expandButtonTextNode,
                elem("span").classes("expand-toggle-button-icon")
            )
            .on("click", () => {
                isExpanded = !isExpanded;
                if (isExpanded) items.classList.remove("no-reveal-animation");
                items.classesIf(isExpanded, "container-expanded");
                expandButton.classesIf(isExpanded, "container-expanded");
                expandButtonTextNode.nodeValue = isExpanded ? "Show less" : "Show more";
            });
    }

    const todoElement = fragment().append(
        inputContainer = elem()
            .classes("todo-input", "flex", "gap-10", "items-center")
            .classesIf(items.children.length > 0, "margin-bottom-15")
            .append(
                elem("button")
                    .classes("todo-add-button", "shrink-0")
                    .attrs({
                        type: "button",
                        "aria-label": "Add task"
                    })
                    .append(elem().classes("todo-plus-icon"))
                    .on("click", () => submitInputValue()),
                input = autoScalingTextarea(textarea => inputArea = textarea
                    .on("keydown", handleInputKeyDown)
                    .attrs({
                        placeholder: "Add a task",
                        spellcheck: "false"
                    })
                ).classes("grow", "min-width-0")
            ),

        reorderable = verticallyReorderable(items, onItemRepositioned, onDragEnd),
    );

    applyCollapsibleState();

    if (useServer) {
        loadFromServer(id).then(data => {
            renderItems(data);
        });
    }

    return todoElement;
}


// See https://css-tricks.com/the-cleanest-trick-for-autogrowing-textareas/
export function autoScalingTextarea(yieldTextarea = null) {
    let textarea, mimic;

    const updateMimic = (newValue) => mimic.text(newValue + ' ');
    const container = elem().classes("auto-scaling-textarea-container").append(
        textarea = elem("textarea")
            .classes("auto-scaling-textarea")
            .on("input", () => updateMimic(textarea.value)),
        mimic = elem().classes("auto-scaling-textarea-mimic")
    )

    if (typeof yieldTextarea === "function") yieldTextarea(textarea);

    return container.component({ setValue: (newValue) => {
        textarea.value = newValue;
        updateMimic(newValue);
    }});
}

export function verticallyReorderable(itemsContainer, onItemRepositioned, onDragEnd) {
    const classToAddToDraggedItem = "is-being-dragged";
    const bodyDraggingClass = "todo-is-dragging";

    const currentlyBeingDragged = {
        element: null,
        initialIndex: null,
        clientOffset: Vec2.new(),
    };

    const decoy = {
        element: null,
        currentIndex: null,
    };

    const draggableContainer = {
        element: null,
        initialRect: null,
    };

    const lastClientPos = Vec2.new();
    let initialScrollY = null;
    let addDocumentEvents, removeDocumentEvents;
    let activePointerId = null;

    const handleReposition = (event) => {
        if (currentlyBeingDragged.element == null) return;

        if (event.clientY !== undefined && event.clientX !== undefined)
            lastClientPos.setFromEvent(event);

        const client = lastClientPos;
        const container = draggableContainer;
        const item = currentlyBeingDragged;

        const scrollOffset = window.scrollY - initialScrollY;
        const offsetY = client.y - container.initialRect.y - item.clientOffset.y + scrollOffset;
        const offsetX = client.x - container.initialRect.x - item.clientOffset.x;

        const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth;
        const viewportWidth = window.innerWidth - scrollbarWidth;

        const confinedX = clamp(
            offsetX,
            -container.initialRect.x,
            viewportWidth - container.initialRect.x - container.initialRect.width
        );

        container.element.styles({
            transform: `translate(${confinedX}px, ${offsetY}px)`,
        });

        const containerTop = client.y - item.clientOffset.y;
        const containerBottom = client.y + container.initialRect.height - item.clientOffset.y;

        let swapWithLast = true;
        let swapWithIndex = null;

        for (let i = 0; i < itemsContainer.children.length; i++) {
            const childRect = itemsContainer.children[i].getBoundingClientRect();
            const topThreshold = childRect.top + childRect.height * .6;
            const bottomThreshold = childRect.top + childRect.height * .4;

            if (containerBottom > topThreshold) {
                if (containerTop < bottomThreshold && i != decoy.currentIndex) {
                    swapWithIndex = i;
                    swapWithLast = false;
                    break;
                }
                continue;
            };

            swapWithLast = false;

            if (i == decoy.currentIndex || i-1 == decoy.currentIndex) break;
            swapWithIndex = (i < decoy.currentIndex) ? i : i-1;
            break;
        }

        const lastItemIndex = itemsContainer.children.length - 1;

        if (swapWithLast && decoy.currentIndex != lastItemIndex)
            swapWithIndex = lastItemIndex;

        if (swapWithIndex === null)
            return;

        const diff = swapWithIndex - decoy.currentIndex;
        if (Math.abs(diff) > 1) {
            swapWithIndex = decoy.currentIndex + Math.sign(diff);
        }

        const siblingToSwapWith = itemsContainer.children[swapWithIndex];

        if (siblingToSwapWith.isCurrentlyAnimating) return;

        const animateDecoy = animateReposition(decoy.element);
        const animateChild = animateReposition(
            siblingToSwapWith,
            () => {
                siblingToSwapWith.isCurrentlyAnimating = false;
                handleReposition({
                    clientX: client.x,
                    clientY: client.y,
                });
            }
        );

        siblingToSwapWith.isCurrentlyAnimating = true;

        if (swapWithIndex > decoy.currentIndex)
            decoy.element.before(siblingToSwapWith);
         else
            decoy.element.after(siblingToSwapWith);

        decoy.currentIndex = itemsContainer.children.indexOf(decoy.element);

        animateDecoy();
        animateChild();
    }

    const handleRelease = () => {
        if (currentlyBeingDragged.element == null) return;

        removeDocumentEvents();
        document.body.classList.remove(bodyDraggingClass);
        const item = currentlyBeingDragged;
        const element = item.element;
        element.styles({ pointerEvents: "none" });
        const animate = animateReposition(element, () => {
            item.element = null;
            element
                .clearClasses(classToAddToDraggedItem)
                .clearStyles("pointer-events");

            if (typeof onDragEnd === "function") onDragEnd(element);

            if (item.initialIndex != decoy.currentIndex && typeof onItemRepositioned === "function")
                onItemRepositioned(element, item.initialIndex, decoy.currentIndex);
        });

        decoy.element.swapWith(element);
        draggableContainer.element.clearStyles("transform", "width");

        item.element = null;

        animate();
    }

    const preventDefault = (event) => {
        event.preventDefault();
    };

    const handleGrab = (event, element) => {
        if (currentlyBeingDragged.element != null) return;

        if (event.buttons !== undefined && (event.buttons & 1) == 0) return;
        activePointerId = event.pointerId;
        event.preventDefault();
        document.body.classList.add(bodyDraggingClass);

        const item = currentlyBeingDragged;
        if (item.element != null) return;

        addDocumentEvents();
        initialScrollY = window.scrollY;
        const client = lastClientPos.setFromEvent(event);
        const elementRect = element.getBoundingClientRect();

        item.element = element;
        item.initialIndex = decoy.currentIndex = itemsContainer.children.indexOf(element);
        item.clientOffset.set(client.x - elementRect.x, client.y - elementRect.y);

        // We use getComputedStyle here to get width and height because .clientWidth and .clientHeight
        // return integers and not the real float values, which can cause the decoy to be off by a pixel
        const elementStyle = getComputedStyle(element);
        const initialWidth = elementStyle.width;

        decoy.element = elem().classes("drag-and-drop-decoy").styles({
            height: elementStyle.height,
            width: initialWidth,
        });

        const container = draggableContainer;

        element.swapWith(decoy.element);
        container.element.append(element);
        element.classes(classToAddToDraggedItem);

        decoy.element.animate({
            keyframes: [{ transform: "scale(.9)", opacity: 0, offset: 0 }],
            options: { duration: 300, easing: "ease" }
        })

        container.element.styles({ width: initialWidth, transform: "none" });
        container.initialRect = container.element.getBoundingClientRect();

        const offsetY = elementRect.y - container.initialRect.y;
        const offsetX = elementRect.x - container.initialRect.x;

        container.element.styles({ transform: `translate(${offsetX}px, ${offsetY}px)` });
    }

    const handlePointerMove = (event) => {
        if (event.pointerId !== undefined && event.pointerId !== activePointerId) return;
        event.preventDefault();
        handleReposition(event);
    };

    const handlePointerUp = (event) => {
        if (event.pointerId !== undefined && event.pointerId !== activePointerId) return;
        activePointerId = null;
        handleRelease(event);
    };

    [addDocumentEvents, removeDocumentEvents] = toggleableEvents(document, {
        "pointermove": handlePointerMove,
        "scroll": handleReposition,
        "pointerdown": preventDefault,
        "contextmenu": preventDefault,
        "pointerup": handlePointerUp,
        "pointercancel": handlePointerUp,
    });

    return elem().classes("drag-and-drop-container").append(
        itemsContainer,
        draggableContainer.element = elem().classes("drag-and-drop-draggable")
    ).component({
        onDragStart: handleGrab
    });
}
