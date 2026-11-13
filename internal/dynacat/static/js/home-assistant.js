(function () {
    const base = (typeof pageData !== 'undefined' && pageData.baseURL) || '';

    // Delegated so toggles keep working after widgets are swapped by dynamic updates.
    document.addEventListener('click', async function (event) {
        const button = event.target.closest('.ha-toggle');
        if (!button || button.disabled) return;

        const row = button.closest('.ha-entity');
        const widget = button.closest('.widget');
        if (!row || !widget) return;

        button.disabled = true;

        try {
            const response = await fetch(base + '/api/widgets/' + widget.dataset.widgetId + '/action/toggle', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ entity: row.dataset.entity }),
            });

            if (!response.ok) {
                console.error('home-assistant: toggle failed', response.status);
                return;
            }

            const state = await response.json();
            button.setAttribute('aria-checked', state.on ? 'true' : 'false');
            row.classList.toggle('ha-entity-inactive', state.inactive);
            row.querySelector('.ha-entity-state').textContent = state.state;
            if (!state.toggleable) button.remove();

            const icon = row.querySelector('.ha-entity-icon');
            if (icon && state.icon) icon.src = state.icon;
        } catch (error) {
            console.error('home-assistant: toggle error', error);
        } finally {
            button.disabled = false;
        }
    });

    document.addEventListener('click', async function (event) {
        const button = event.target.closest('.ha-option');
        if (!button || button.disabled || button.getAttribute('aria-pressed') === 'true') return;

        const row = button.closest('.ha-entity');
        const widget = button.closest('.widget');
        if (!row || !widget) return;

        const buttons = row.querySelectorAll('.ha-option');
        buttons.forEach(b => b.disabled = true);

        try {
            const response = await fetch(base + '/api/widgets/' + widget.dataset.widgetId + '/action/select', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ entity: row.dataset.entity, option: button.dataset.option }),
            });

            if (!response.ok) {
                console.error('home-assistant: select failed', response.status);
                return;
            }

            const state = await response.json();
            buttons.forEach(b => b.setAttribute('aria-pressed', b.dataset.option === state.selected ? 'true' : 'false'));
            row.classList.toggle('ha-entity-inactive', state.inactive);

            const icon = row.querySelector('.ha-entity-icon');
            if (icon && state.icon) icon.src = state.icon;
        } catch (error) {
            console.error('home-assistant: select error', error);
        } finally {
            buttons.forEach(b => b.disabled = false);
        }
    });
})();
