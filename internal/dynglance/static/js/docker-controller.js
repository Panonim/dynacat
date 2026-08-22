(function () {
    const base = (typeof pageData !== 'undefined' && pageData.baseURL) || '';

    // activePulls: widgetId -> [{pullId, image, statusText, isError}]
    const activePulls = new Map();
    // MutationObservers watching widgets for content replacements
    const widgetObservers = new Map();
    // confirmingItems: "widgetId:type:id" -> {origHTML, origTitle, timeoutId}
    const confirmingItems = new Map();

    function escapeHtml(str) {
        return String(str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;');
    }

    // Find the images <ul> list inside a widget (the section that has the pull input)
    function getImagesListForWidget(widgetId) {
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return null;
        for (const section of widget.querySelectorAll('.docker-ctrl-section')) {
            if (section.querySelector('.docker-ctrl-pull')) {
                return section.querySelector('.list');
            }
        }
        return null;
    }

    function setPullEntryState(entry, pull) {
        const spinner = entry.querySelector('.docker-ctrl-pull-spinner');
        const status = entry.querySelector('.docker-ctrl-pull-status');
        const statusText = pull.statusText || 'Pulling\u2026';

        if (status) status.textContent = statusText;

        if (spinner) {
            spinner.style.visibility = statusText === 'Pulling\u2026' ? '' : 'hidden';
        }

        entry.classList.toggle('docker-ctrl-pull-error', !!pull.isError);
    }

    function createPullListItem(pull) {
        const li = document.createElement('li');
        li.className = 'docker-ctrl-row docker-ctrl-pull-entry flex items-center gap-10';
        li.dataset.pullId = pull.pullId;
        li.innerHTML =
            '<div class="shrink-0">' +
                '<div class="loading-icon docker-ctrl-pull-spinner" aria-hidden="true"></div>' +
            '</div>' +
            '<div class="min-width-0 grow">' +
                '<div class="color-highlight text-truncate">' + escapeHtml(pull.image) + '</div>' +
                '<div class="docker-ctrl-pull-status size-h5 color-subdue"></div>' +
            '</div>';
        setPullEntryState(li, pull);
        return li;
    }

    function updateNoImagesState(widgetId) {
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return;
        const noImages = widget.querySelector('.docker-ctrl-no-images');
        const list = getImagesListForWidget(widgetId);
        if (!noImages || !list) return;

        const hasEntries = list.children.length > 0;
        noImages.classList.toggle('hidden', hasEntries);
    }

    function getPull(widgetId, pullId) {
        const pulls = activePulls.get(widgetId);
        if (!pulls) return null;
        return pulls.find(function (pull) { return pull.pullId === pullId; }) || null;
    }

    function isImagePresentInWidget(widgetId, imageName) {
        const list = getImagesListForWidget(widgetId);
        if (!list) return false;

        const expected = String(imageName || '').trim().toLowerCase();
        if (!expected) return false;

        const rows = list.querySelectorAll('.docker-ctrl-row:not(.docker-ctrl-pull-entry)');
        for (const row of rows) {
            const label = row.querySelector('.color-highlight');
            if (!label) continue;
            if (label.textContent && label.textContent.trim().toLowerCase() === expected) {
                return true;
            }
        }

        return false;
    }

    function injectActivePulls(widgetId) {
        const pulls = activePulls.get(widgetId);
        if (!pulls || !pulls.length) return;
        const list = getImagesListForWidget(widgetId);
        if (!list) return;
        for (const pull of pulls) {
            if (isImagePresentInWidget(widgetId, pull.image)) {
                setTimeout(function () { removePullEntry(widgetId, pull.pullId); }, 100);
                continue;
            }

            const existing = list.querySelector('[data-pull-id="' + pull.pullId + '"]');
            if (!existing) {
                list.appendChild(createPullListItem(pull));
            } else {
                setPullEntryState(existing, pull);
            }
        }
        updateNoImagesState(widgetId);
    }

    // Watch a widget's direct children for replacements (i.e. widget-content swaps)
    // and re-inject active pull entries after each swap.
    function watchWidget(widgetId) {
        if (widgetObservers.has(widgetId)) return;
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return;
        const obs = new MutationObserver(function () {
            injectActivePulls(widgetId);
        });
        obs.observe(widget, { childList: true });
        widgetObservers.set(widgetId, obs);
    }

    function unwatchWidget(widgetId) {
        const obs = widgetObservers.get(widgetId);
        if (obs) { obs.disconnect(); widgetObservers.delete(widgetId); }
    }

    function removePullEntry(widgetId, pullId) {
        const pulls = activePulls.get(widgetId);
        if (pulls) {
            const idx = pulls.findIndex(function (p) { return p.pullId === pullId; });
            if (idx !== -1) pulls.splice(idx, 1);
            if (!pulls.length) { activePulls.delete(widgetId); unwatchWidget(widgetId); }
        }
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (widget) {
            const entry = widget.querySelector('[data-pull-id="' + pullId + '"]');
            if (entry) entry.remove();
        }
        updateNoImagesState(widgetId);
    }

    // Fetch fresh widget content and replace only the row for the given container ID.
    // For 'remove' actions, the row is simply deleted from the DOM without a server fetch.
    async function updateContainerRow(widgetId, id, action) {
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return;

        if (action === 'remove') {
            const row = widget.querySelector('[data-container-id="' + id + '"]');
            if (row) row.remove();
            return;
        }

        try {
            const resp = await fetch(base + '/api/widgets/' + widgetId + '/content/');
            if (!resp.ok) return;
            const html = await resp.text();
            const tmp = document.createElement('div');
            tmp.innerHTML = html;
            const freshWidget = tmp.querySelector('.widget[data-widget-id="' + widgetId + '"]');
            if (!freshWidget) return;

            const oldRow = widget.querySelector('[data-container-id="' + id + '"]');
            const newRow = freshWidget.querySelector('[data-container-id="' + id + '"]');

            if (oldRow && newRow) {
                oldRow.replaceWith(newRow);
            } else if (oldRow && !newRow) {
                oldRow.remove();
            }

            if (typeof window.dynglanceSetupPopovers === 'function') {
                window.dynglanceSetupPopovers();
            }
        } catch (e) {
            console.error('docker-ctrl: row update error', e);
        }
    }

    window.dockerCtrlAction = async function (btn, widgetId, id, action) {
        btn.disabled = true;
        try {
            const resp = await fetch(
                base + '/api/widgets/' + widgetId + '/action/containers/' + id + '/' + action,
                { method: 'POST' }
            );
            if (!resp.ok) {
                console.error('docker-ctrl: action failed', resp.status);
                btn.disabled = false;
                return;
            }
        } catch (e) {
            console.error('docker-ctrl: action error', e);
            btn.disabled = false;
            return;
        }
        // Give Docker a moment to apply the state change before fetching fresh state
        await new Promise(function (r) { setTimeout(r, 400); });
        await updateContainerRow(widgetId, id, action);
    };

    function applyConfirmVisual(btn) {
        btn.dataset.confirming = '1';
        btn.classList.add('confirm-pending');
        btn.title = 'Click again to confirm removal';
        btn.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path fill-rule="evenodd" d="M19.916 4.626a.75.75 0 0 1 .208 1.04l-9 13.5a.75.75 0 0 1-1.154.114l-6-6a.75.75 0 0 1 1.06-1.06l5.353 5.353 8.493-12.74a.75.75 0 0 1 1.04-.207Z" clip-rule="evenodd" /></svg>';
    }

    function clearConfirmVisual(btn, origHTML, origTitle) {
        btn.disabled = false;
        delete btn.dataset.confirming;
        btn.classList.remove('confirm-pending');
        btn.title = origTitle;
        btn.innerHTML = origHTML;
    }

    function showRemoveError(widgetId, type, id, msg) {
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return;
        const attr = type === 'containers' ? '[data-container-id="' + id + '"]' : '[data-image-id="' + id + '"]';
        const row = widget.querySelector(attr);
        if (!row) return;
        const existing = row.querySelector('.docker-ctrl-remove-error');
        if (existing) existing.remove();
        const err = document.createElement('div');
        err.className = 'docker-ctrl-remove-error size-h5 color-negative';
        err.textContent = msg;
        row.appendChild(err);
        setTimeout(function () { err.remove(); }, 5000);
    }

    function findRemoveBtn(widgetId, type, id) {
        const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
        if (!widget) return null;
        const attr = type === 'containers' ? '[data-container-id="' + id + '"]' : '[data-image-id="' + id + '"]';
        const row = widget.querySelector(attr);
        return row ? row.querySelector('.docker-ctrl-icon-btn.remove') : null;
    }

    // After a widget update, re-apply confirming visuals to any buttons that were in confirm state.
    document.addEventListener('dynglance:widget-updated', function (event) {
        const widget = event.detail?.widget;
        const widgetId = event.detail?.widgetId || widget?.dataset?.widgetId;
        if (!widgetId) return;
        for (const [key] of confirmingItems) {
            const [wid, type, id] = key.split(':');
            if (wid !== widgetId) continue;
            const btn = findRemoveBtn(widgetId, type, id);
            if (btn) applyConfirmVisual(btn);
        }
    });

    window.dockerCtrlConfirmRemove = function (btn, widgetId, id, type) {
        const key = widgetId + ':' + type + ':' + id;

        if (confirmingItems.has(key)) {
            // Second click — perform the removal
            const state = confirmingItems.get(key);
            clearTimeout(state.timeoutId);
            confirmingItems.delete(key);
            btn.disabled = true;
            const actionPath = type === 'containers'
                ? 'containers/' + id + '/remove'
                : 'images/' + id + '/remove';
            fetch(base + '/api/widgets/' + widgetId + '/action/' + actionPath, { method: 'POST' })
                .then(function (resp) {
                    if (!resp.ok) {
                        clearConfirmVisual(btn, state.origHTML, state.origTitle);
                        return resp.text().then(function (msg) {
                            showRemoveError(widgetId, type, id, msg.trim() || 'Remove failed');
                        });
                    }
                    if (type === 'containers') {
                        updateContainerRow(widgetId, id, 'remove');
                    } else {
                        window.dynglanceRefreshWidget && window.dynglanceRefreshWidget(widgetId);
                    }
                })
                .catch(function (e) {
                    clearConfirmVisual(btn, state.origHTML, state.origTitle);
                    console.error('docker-ctrl: remove error', e);
                });
            return;
        }

        const origHTML = btn.innerHTML;
        const origTitle = btn.title;
        applyConfirmVisual(btn);

        const timeoutId = setTimeout(function () {
            confirmingItems.delete(key);
            const freshBtn = findRemoveBtn(widgetId, type, id);
            if (freshBtn) clearConfirmVisual(freshBtn, origHTML, origTitle);
        }, 3000);

        confirmingItems.set(key, { origHTML, origTitle, timeoutId });
    };

    async function pollPullStatus(widgetId, pullId) {
        try {
            const resp = await fetch(base + '/api/widgets/' + widgetId + '/action/images/pull/' + pullId + '/status');
            if (resp.status === 404) { removePullEntry(widgetId, pullId); return; }
            if (!resp.ok) { setTimeout(function () { pollPullStatus(widgetId, pullId); }, 2000); return; }
            const data = await resp.json();

            const pull = getPull(widgetId, pullId);
            if (!pull) {
                removePullEntry(widgetId, pullId);
                return;
            }

            if (isImagePresentInWidget(widgetId, pull.image)) {
                pull.statusText = 'Done';
                pull.isError = false;
                const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
                const entry = widget && widget.querySelector('[data-pull-id="' + pullId + '"]');
                if (entry) setPullEntryState(entry, pull);
                setTimeout(function () { removePullEntry(widgetId, pullId); }, 300);
                return;
            }

            if (data.done) {
                const widget = document.querySelector('.widget[data-widget-id="' + widgetId + '"]');
                const entry = widget && widget.querySelector('[data-pull-id="' + pullId + '"]');
                if (data.error) {
                    pull.statusText = 'Failed';
                    pull.isError = true;
                    if (entry) setPullEntryState(entry, pull);
                    setTimeout(function () { removePullEntry(widgetId, pullId); }, 3000);
                } else {
                    pull.statusText = 'Done';
                    pull.isError = false;
                    if (entry) setPullEntryState(entry, pull);
                    setTimeout(function () { removePullEntry(widgetId, pullId); }, 1500);
                }
                return;
            }
        } catch (e) {
            console.error('docker-ctrl: pull status error', e);
        }
        setTimeout(function () { pollPullStatus(widgetId, pullId); }, 1000);
    }

    window.dockerCtrlPull = async function (btn, widgetId) {
        const input = document.getElementById('docker-ctrl-pull-' + widgetId);
        if (!input) return;
        const image = input.value.trim();
        if (!image) return;
        input.value = '';

        try {
            const resp = await fetch(base + '/api/widgets/' + widgetId + '/action/images/pull', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ image: image }),
            });
            if (!resp.ok) { console.error('docker-ctrl: pull failed', resp.status); return; }
            const data = await resp.json();
            const pullId = data.pullId;

            if (!activePulls.has(widgetId)) activePulls.set(widgetId, []);
            activePulls.get(widgetId).push({
                pullId: pullId,
                image: image,
                statusText: 'Pulling\u2026',
                isError: false,
            });
            watchWidget(widgetId);
            injectActivePulls(widgetId);
            pollPullStatus(widgetId, pullId);
        } catch (e) {
            console.error('docker-ctrl: pull error', e);
        }
    };
}());
