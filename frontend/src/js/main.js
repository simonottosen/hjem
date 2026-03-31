import { ScatterPricesChart } from './scatter_prices.js';
import { SquareMeterPricesChart } from './sqmeter_prices.js';

const updates = [
    ScatterPricesChart('prices'),
    SquareMeterPricesChart('sqmeters')
]

const loader = document.getElementById( "loader-icon" );
const errorbox = document.getElementById( "error-msg" );
const datasets = document.getElementById( "datasets" );
const csvlink = document.getElementById( "csvlink" );
const progressFill = document.getElementById( "progress-bar-fill" );
const progressText = document.getElementById( "progress-text" );
const progressTime = document.getElementById( "progress-time" );
const errorTrans = {
    "non-unique address": "Der findes flere addresser med den beskrivelse, vær mere præcis",
    "no found address": "Kunne ikke finde nogen addresser udfra den søgning"
}

const endpoint = '';

function formatTime(ms) {
    var secs = Math.floor(ms / 1000);
    if (secs < 60) return secs + 's';
    var mins = Math.floor(secs / 60);
    secs = secs % 60;
    return mins + 'm ' + secs + 's';
}

function updateProgressUI(data) {
    if (data.total > 0 && data.current > 0) {
        var pct = Math.round((data.current / data.total) * 100);
        progressFill.style.width = pct + '%';
        progressFill.classList.remove('indeterminate');
    } else {
        progressFill.style.width = '';
        progressFill.classList.add('indeterminate');
    }

    progressText.textContent = data.message || 'Arbejder...';

    if (data.elapsed_ms > 0) {
        var elapsed = formatTime(data.elapsed_ms);
        var timeStr = 'Tid: ' + elapsed;

        if (data.total > 0 && data.current > 0 && data.current < data.total) {
            var msPerItem = data.elapsed_ms / data.current;
            var remaining = msPerItem * (data.total - data.current);
            timeStr += ' | Ca. ' + formatTime(remaining) + ' tilbage';
        }

        progressTime.textContent = timeStr;
    }
}

function performSearch() {
    loader.style.display = '';
    errorbox.style.display = 'none';
    datasets.style.display = 'none';

    // Reset progress bar
    progressFill.style.width = '0%';
    progressFill.classList.add('indeterminate');
    progressText.textContent = 'Søger...';
    progressTime.textContent = '';

    var evtSource = new EventSource(endpoint + '/api/progress');
    evtSource.onmessage = function(event) {
        var data = JSON.parse(event.data);
        updateProgressUI(data);
        if (data.stage === 'done' || data.stage === 'error') {
            evtSource.close();
        }
    };
    evtSource.onerror = function() {
        evtSource.close();
    };

    const fd = new FormData(form);
    const query = fd.get("query");
    const filter = Number(fd.get("filter"));
    const range = Number(fd.get("range"));

    const XHR = new XMLHttpRequest();

    XHR.addEventListener( "load", function(event) {
        evtSource.close();
        loader.style.display = 'none';
        const resp = JSON.parse(event.target.responseText);

        if (resp.error !== undefined) {
            const err = resp.error;
            errorbox.innerHTML = err;

            for (const s in errorTrans) {
                if(err.toLowerCase().includes(s)) {
                    errorbox.innerHTML = errorTrans[s];
                    break
                }
            }

            errorbox.style.display = '';
            return
        }

        var csvUrl = endpoint + "/download/csv?q=" +
            encodeURIComponent(query) + "&range=" +
            encodeURIComponent(range);
        csvlink.href = csvUrl;

        datasets.style.display = '';

        for (const f of updates) {
            f(resp);
        }
    });

    XHR.addEventListener( "error", function( event ) {
        evtSource.close();
        loader.style.display = 'none';
        errorbox.innerHTML = "Kan ikke forbinde til backend";
        errorbox.style.display = '';
    } );

    XHR.open( "POST", endpoint + "/api/lookup" );
    XHR.setRequestHeader("Content-Type", "application/json");
    XHR.send(JSON.stringify({
        "q": query,
        "ranges": [range],
        "filter_below_std": filter,
    }));
}

const form = document.getElementById( "search" );

form.addEventListener( "submit", function ( event ) {
    event.preventDefault();
    performSearch();
});
