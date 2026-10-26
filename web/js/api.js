// HTTP client for the Calendar API. Every call returns the decoded JSON or
// throws an ApiError whose message can be shown to the user.

import { TZ } from './dates.js';

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

let unauthorizedHandler = () => {};

/** Called whenever the server says the session is no longer valid. */
export function onUnauthorized(fn) {
  unauthorizedHandler = fn;
}

async function request(method, path, body) {
  const options = { method, credentials: 'same-origin', headers: {} };
  if (body instanceof FormData) {
    options.body = body;
  } else if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(path, options);
  } catch {
    throw new ApiError('Cannot reach the server. Check your connection.', 0);
  }

  let data = null;
  if ((res.headers.get('Content-Type') || '').includes('application/json')) {
    data = await res.json().catch(() => null);
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/auth/')) unauthorizedHandler();
    throw new ApiError(data?.error || `Request failed (${res.status})`, res.status);
  }
  return data;
}

const get = (path) => request('GET', path);
const post = (path, body = {}) => request('POST', path, body);
const patch = (path, body) => request('PATCH', path, body);
const del = (path) => request('DELETE', path);
const id = encodeURIComponent;

export const api = {
  auth: {
    status: () => get('/api/auth/status'),
    setup: (password) => post('/api/auth/setup', { password }),
    login: (password) => post('/api/auth/login', { password }),
    logout: () => post('/api/auth/logout'),
    changePassword: (oldPassword, newPassword) =>
      post('/api/auth/change-password', { old_password: oldPassword, new_password: newPassword }),
  },

  info: () => get('/api/info'),

  calendars: {
    list: () => get('/api/calendars'),
    create: (name, color) => post('/api/calendars', { name, color }),
    subscribe: (name, url, color) => post('/api/subscriptions', { name, url, color }),
    update: (calendarId, fields) => patch(`/api/calendars/${id(calendarId)}`, fields),
    remove: (calendarId) => del(`/api/calendars/${id(calendarId)}`),
    refresh: (calendarId) => post(`/api/calendars/${id(calendarId)}/refresh`),
    exportURL: (calendarId) => `/api/calendars/${id(calendarId)}/export`,
    importFile: (calendarId, file) => {
      const form = new FormData();
      form.append('file', file);
      return request('POST', `/api/calendars/${id(calendarId)}/import`, form);
    },
  },

  events: {
    /** Occurrences between two days (to exclusive). */
    list: (from, to) => get(`/api/events?from=${from}&to=${to}&tz=${id(TZ)}`),
    create: (event) => post('/api/events', { ...event, tz: TZ }),
    /** event: the full event; scope: 'all' | 'this'; calendar_id may move it. */
    update: (calendarId, name, event) => patch(`/api/events/${id(calendarId)}/${id(name)}`, { ...event, tz: TZ }),
    remove: (calendarId, name, { scope = 'all', recurrenceId = '' } = {}) =>
      del(`/api/events/${id(calendarId)}/${id(name)}?scope=${scope}&recurrence_id=${id(recurrenceId)}`),
  },

  search: (query) => get(`/api/search?q=${id(query)}`),
};
