import { find } from "./templating.js";

const SUBMIT_ENDPOINT = pageData.baseURL + "/api/config-upload";

const passphraseInput = find("#config-upload-passphrase");
const filenameRow = find("#config-upload-filename-row");
const filenameInput = find("#config-upload-filename");
const dropzone = find("#config-upload-dropzone");
const fileInput = find("#config-upload-file-input");
const filenamePreview = find("#config-upload-filename-preview");
const messageBox = find("#config-upload-message");
const submitButton = find("#config-upload-submit");
const modeInputs = document.getElementsByName("config-upload-mode");

const state = {
    fileContent: null,
    fileName: "",
    isSubmitting: false,
};

function currentMode() {
    for (const input of modeInputs) {
        if (input.checked) return input.value;
    }
    return "replace";
}

function updateFilenameRowVisibility() {
    filenameRow.showIf(currentMode() === "include");
}

for (const input of modeInputs) {
    input.addEventListener("change", updateFilenameRowVisibility);
}
updateFilenameRowVisibility();

function setMessage(text, isError) {
    messageBox.text(text || "");
    messageBox.classesIf(!!isError, "login-error-message");
    if (!isError) messageBox.clearClasses("login-error-message");
}

function updateSubmitEnabled() {
    const hasFile = state.fileContent !== null;
    const hasPassphrase = passphraseInput.value.trim().length > 0;
    const modeOk = currentMode() !== "include" || filenameInput.value.trim().length > 0;
    submitButton.disabled = !(hasFile && hasPassphrase && modeOk && !state.isSubmitting);
}

passphraseInput.on("input", updateSubmitEnabled);
filenameInput.on("input", updateSubmitEnabled);
for (const input of modeInputs) {
    input.addEventListener("change", updateSubmitEnabled);
}

function loadFile(file) {
    if (!file) return;

    if (!/\.(ya?ml)$/i.test(file.name)) {
        setMessage("Please choose a .yml or .yaml file", true);
        return;
    }

    const reader = new FileReader();
    reader.onload = () => {
        state.fileContent = reader.result;
        state.fileName = file.name;
        filenamePreview.text("Selected: " + file.name);

        if (currentMode() === "include" && !filenameInput.value.trim()) {
            filenameInput.value = file.name;
        }

        setMessage("");
        updateSubmitEnabled();
    };
    reader.onerror = () => setMessage("Could not read file", true);
    reader.readAsText(file);
}

fileInput.on("change", () => loadFile(fileInput.files[0]));

dropzone.on("dragover", (e) => {
    e.preventDefault();
    dropzone.classes("config-upload-dropzone-active");
});

dropzone.on("dragleave", () => {
    dropzone.clearClasses("config-upload-dropzone-active");
});

dropzone.on("drop", (e) => {
    e.preventDefault();
    dropzone.clearClasses("config-upload-dropzone-active");
    const file = e.dataTransfer?.files?.[0];
    loadFile(file);
});

submitButton.on("click", async () => {
    if (submitButton.disabled) return;

    state.isSubmitting = true;
    submitButton.disable();
    setMessage("Uploading...");

    const mode = currentMode();

    try {
        const response = await fetch(SUBMIT_ENDPOINT, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
                passphrase: passphraseInput.value,
                mode,
                filename: mode === "include" ? filenameInput.value.trim() : "",
                content: state.fileContent,
            }),
        });

        if (response.status === 401) {
            setMessage("Incorrect passphrase", true);
        } else if (response.status === 429) {
            setMessage("Too many attempts, try again in a few minutes", true);
        } else {
            const data = await response.json().catch(() => null);
            if (response.ok && data && data.ok) {
                let text = data.message || "Done";
                if (data.includeLine) text += "\n\n" + data.includeLine;
                setMessage(text, false);
            } else {
                setMessage((data && data.error) || "Something went wrong", true);
            }
        }
    } catch (err) {
        setMessage("Network error, please try again", true);
    } finally {
        state.isSubmitting = false;
        updateSubmitEnabled();
    }
});
