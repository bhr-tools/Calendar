// Run with: node --test tests/js/*.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseQuickAdd } from '../../web/js/quickadd.js';
import { hhmm, isoDate } from '../../web/js/dates.js';

const today = '2026-10-26'; // a Monday

function check(text, want) {
  const r = parseQuickAdd(text, { today });
  const got = {
    title: r.title,
    day: isoDate(r.start),
    time: r.allDay ? 'all day' : `${hhmm(r.start)}-${hhmm(r.end)}`,
  };
  if (want.rrule !== undefined) got.rrule = r.rrule;
  if (want.location !== undefined) got.location = r.location;
  assert.deepEqual(got, want, text);
}

test('time ranges', () => {
  check('Sport demain 18h-19h30 @Gym', { title: 'Sport', day: '2026-10-27', time: '18:00-19:30', location: 'Gym' });
  check('Meeting 14-16h', { title: 'Meeting', day: today, time: '14:00-16:00' });
  check('Call from 2pm to 3:30pm friday', { title: 'Call', day: '2026-10-30', time: '14:00-15:30' });
  check('Cours de 8h à 9h45 jeudi', { title: 'Cours', day: '2026-10-29', time: '08:00-09:45' });
});

test('single time and durations', () => {
  check('Deep work lundi 9h pour 2h', { title: 'Deep work', day: '2026-11-02', time: '09:00-11:00' });
  check('Dentist 15/11 10:30', { title: 'Dentist', day: '2026-11-15', time: '10:30-11:30' });
  check('Break 16h 20min', { title: 'Break', day: today, time: '16:00-16:20' });
  check('Read at 21h for 1h30', { title: 'Read', day: today, time: '21:00-22:30' });
});

test('all day and recurrence', () => {
  check('Holidays 24 dec', { title: 'Holidays', day: '2026-12-24', time: 'all day' });
  check('Standup every weekday 9:30', { title: 'Standup', day: today, time: '09:30-10:30', rrule: 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR' });
  check('Gym tous les mercredis 19h', { title: 'Gym', day: '2026-10-28', time: '19:00-20:00', rrule: 'FREQ=WEEKLY;BYDAY=WE' });
  check('Plain title', { title: 'Plain title', day: today, time: 'all day' });
});

test('numbers in titles are left alone', () => {
  check('Read chapter 12', { title: 'Read chapter 12', day: today, time: 'all day' });
});
