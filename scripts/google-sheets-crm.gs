// Paste this file into Extensions → Apps Script of the Google Sheet.
// Replace the value below with a long random string, then use the same value
// in SHEETS_WEBHOOK_SECRET in .env. Do not share it publicly.
const WEBHOOK_SECRET = 'replace-with-a-long-random-secret';
// Copy the part between /d/ and /edit from the Google Sheet URL here.
const SPREADSHEET_ID = 'paste-google-sheet-id-here';
const TAB_NAME = 'CRM';

const HEADER = [
  'lead_id', 'created_at', 'updated_at', 'source', 'name', 'email',
  'nationality', 'current_country', 'service', 'move_date', 'family_size',
  'job_offer', 'message', 'gdpr_consent', 'consent_version', 'consented_at',
  'category', 'urgency', 'estimate_min_eur', 'estimate_max_eur', 'summary',
  'status', 'assigned_to',
];

function doPost(event) {
  try {
    const request = JSON.parse(event.postData.contents);
    if (!request.secret || request.secret !== WEBHOOK_SECRET) throw new Error('unauthorized');

    const sheet = getCRMTab_();
    const action = request.action;
    if (action === 'upsert') upsert_(sheet, request.lead);
    else if (action === 'claim') updateStatus_(sheet, request.lead_id, 'claimed', request.manager || 'manager');
    else if (action === 'spam') updateStatus_(sheet, request.lead_id, 'spam', '');
    else throw new Error('unknown action');

    return json_({ok: true});
  } catch (error) {
    return json_({ok: false, error: String(error.message || error)});
  }
}

function getCRMTab_() {
  if (SPREADSHEET_ID === 'paste-google-sheet-id-here') throw new Error('set SPREADSHEET_ID first');
  const spreadsheet = SpreadsheetApp.openById(SPREADSHEET_ID);
  const sheet = spreadsheet.getSheetByName(TAB_NAME) || spreadsheet.insertSheet(TAB_NAME);
  const existing = sheet.getRange(1, 1, 1, HEADER.length).getValues()[0];
  if (existing.every((value) => value === '')) sheet.getRange(1, 1, 1, HEADER.length).setValues([HEADER]);
  else if (existing.join('|') !== HEADER.join('|')) throw new Error(`tab ${TAB_NAME} has an incompatible header`);
  sheet.setFrozenRows(1);
  return sheet;
}

function upsert_(sheet, lead) {
  if (!lead || !lead.lead_id) throw new Error('lead_id is required');
  const found = findRow_(sheet, lead.lead_id);
  const previous = found ? sheet.getRange(found, 1, 1, HEADER.length).getValues()[0] : [];
  const status = previous[21] || 'new';
  const assignedTo = previous[22] || '';
  const now = new Date().toISOString();
  const row = [
    lead.lead_id, lead.created_at, now, lead.source, lead.name, lead.email,
    lead.nationality, lead.current_country, lead.service, lead.move_date,
    lead.family_size, lead.job_offer, lead.message, lead.gdpr_consent,
    lead.consent_version, lead.consented_at, lead.category, lead.urgency,
    lead.estimate_min_eur, lead.estimate_max_eur, lead.summary, status, assignedTo,
  ];
  if (found) sheet.getRange(found, 1, 1, HEADER.length).setValues([row]);
  else sheet.appendRow(row);
}

function updateStatus_(sheet, leadID, status, manager) {
  const rowNumber = findRow_(sheet, leadID);
  if (!rowNumber) throw new Error(`lead ${leadID} not found`);
  const row = sheet.getRange(rowNumber, 1, 1, HEADER.length).getValues()[0];
  row[2] = new Date().toISOString();
  row[21] = status;
  row[22] = manager;
  sheet.getRange(rowNumber, 1, 1, HEADER.length).setValues([row]);
}

function findRow_(sheet, leadID) {
  const lastRow = sheet.getLastRow();
  if (lastRow < 2) return 0;
  const ids = sheet.getRange(2, 1, lastRow - 1, 1).getValues();
  const index = ids.findIndex((row) => String(row[0]) === String(leadID));
  return index < 0 ? 0 : index + 2;
}

function json_(value) {
  return ContentService.createTextOutput(JSON.stringify(value)).setMimeType(ContentService.MimeType.JSON);
}
