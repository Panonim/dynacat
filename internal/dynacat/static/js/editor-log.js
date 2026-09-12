// The editor and the widget builder only have room for one short line of feedback, so the full
// picture - what was sent, what came back and which config key the server blamed - goes here.

const BADGE = "background:#6d28d9;color:#fff;padding:1px 6px;border-radius:3px;font-weight:600";
const LABEL = "font-weight:600";

function isEmpty(value) {
    if (value === undefined || value === null || value === "") return true;
    return typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 0;
}

// One collapsed group per failure 
export function logFailure(action, detail) {
    console.groupCollapsed(`%cdynacat editor%c ${action}`, BADGE, "");
    for (const [key, value] of Object.entries(detail)) {
        if (!isEmpty(value)) console.log(`%c${key}:`, LABEL, value);
    }
    console.groupEnd();
}

// The parts of a rejected editor response worth printing
export function responseDetail(method, url, res, body) {
    return {
        request: `${method} ${url}`,
        status: `${res.status} ${res.statusText}`,
        message: body?.error,
        code: body?.code,
        field: body?.field,
        hint: body?.hint,
        context: body?.context,
        response: body,
    };
}
