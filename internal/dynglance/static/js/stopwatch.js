const lapIconSvg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor">
  <path d="M3.5 2.75a.75.75 0 0 0-1.5 0v14.5a.75.75 0 0 0 1.5 0v-4.392l1.657-.348a6.449 6.449 0 0 1 4.271.572 7.948 7.948 0 0 0 5.965.524l2.078-.64A.75.75 0 0 0 18 12.25v-8.5a.75.75 0 0 0-.904-.734l-2.38.501a7.25 7.25 0 0 1-4.186-.363l-.502-.2a8.75 8.75 0 0 0-5.053-.439L3.5 2.843V2.75Z" />
</svg>`;

function formatTime(ms) {
    const totalSeconds = Math.floor(ms / 1000);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
}

export default function(container) {
    const display = container.querySelector('[data-stopwatch-display]');
    const toggleBtn = container.querySelector('[data-stopwatch-toggle]');
    const resetBtn = container.querySelector('[data-stopwatch-reset]');
    const lapBtn = container.querySelector('[data-stopwatch-lap]');
    const lapsList = container.querySelector('[data-stopwatch-laps]');
    const iconPlay = toggleBtn.querySelector('.stopwatch-icon-play');
    const iconPause = toggleBtn.querySelector('.stopwatch-icon-pause');
    const startOnOpen = container.dataset.stopwatchStartOnOpen === 'true';

    let startTime = null;
    let elapsed = 0;
    let running = false;
    let rafId = null;
    let lapCount = 0;

    function tick() {
        display.textContent = formatTime(elapsed + (performance.now() - startTime));
        rafId = requestAnimationFrame(tick);
    }

    function toggle() {
        if (running) {
            running = false;
            elapsed += performance.now() - startTime;
            startTime = null;
            cancelAnimationFrame(rafId);
            rafId = null;
            display.textContent = formatTime(elapsed);
            iconPause.classList.add('stopwatch-btn-hidden');
            iconPlay.classList.remove('stopwatch-btn-hidden');
            toggleBtn.title = 'Resume';
        } else {
            running = true;
            startTime = performance.now();
            rafId = requestAnimationFrame(tick);
            iconPlay.classList.add('stopwatch-btn-hidden');
            iconPause.classList.remove('stopwatch-btn-hidden');
            toggleBtn.title = 'Pause';
        }
    }

    function reset() {
        if (running) {
            running = false;
            cancelAnimationFrame(rafId);
            rafId = null;
            iconPause.classList.add('stopwatch-btn-hidden');
            iconPlay.classList.remove('stopwatch-btn-hidden');
            toggleBtn.title = 'Start';
        }
        elapsed = 0;
        lapCount = 0;
        startTime = null;
        display.textContent = '0:00:00';
        lapsList.innerHTML = '';
    }

    function lap() {
        const current = running
            ? elapsed + (performance.now() - startTime)
            : elapsed;

        if (current === 0) return;

        lapCount++;
        const li = document.createElement('li');
        li.className = 'stopwatch-lap';
        li.innerHTML = `${lapIconSvg}<span>${lapCount}.</span><span class="stopwatch-lap-time">${formatTime(current)}</span>`;
        lapsList.prepend(li);
    }

    toggleBtn.addEventListener('click', toggle);
    resetBtn.addEventListener('click', reset);
    lapBtn.addEventListener('click', lap);

    if (startOnOpen) {
        toggle();
    }
}
