import exec from 'k6/execution';

const baseUrl = __ENV.E2E_TRAEFIK_PUBLIC_URL || __ENV.E2E_TRAEFIK_BASE_URL || 'http://traefik:80';
const serviceCount = Number(__ENV.E2E_JOB_COUNT || 10);
const fixedServiceName = __ENV.E2E_K6_SERVICE_NAME || '';
const targetMode = __ENV.E2E_K6_TARGET_MODE || (fixedServiceName ? 'fixed' : 'random');
const roundRobinOffset = Number(__ENV.E2E_K6_ROUND_ROBIN_OFFSET || 0);
const roundRobinWidth = Number(__ENV.E2E_K6_ROUND_ROBIN_WIDTH || 0);
const gateSuccessRate = Number(__ENV.E2E_GATE_TRAFFIC_SUCCESS_RATE || 0.95);
const gateWakeP95Ms = Number(__ENV.E2E_GATE_WAKE_P95_MS || 45000);
const gateWakeP99Ms = Number(__ENV.E2E_GATE_WAKE_P99_MS || 60000);
const gateMaxFailureRate = Number(Math.max(0, 1 - gateSuccessRate).toFixed(6));

export function configuredServiceCount() {
  return serviceCount;
}

export function serviceNameFromNumber(serviceNumber) {
  return `echo-s2z-${String(serviceNumber).padStart(4, '0')}`;
}

export function serviceNameFromIndex(index = 0) {
  return serviceNameFromNumber((index % serviceCount) + 1);
}

export function selectedTargetMode() {
  return targetMode;
}

export function resolvedBaseUrl() {
  return baseUrl;
}

function finiteOrZero(value) {
  return Number.isFinite(value) ? Math.floor(value) : 0;
}

function roundRobinIteration() {
  const scenarioState = exec.scenario || {};
  if (Number.isFinite(scenarioState.iterationInTest)) {
    return finiteOrZero(scenarioState.iterationInTest) + roundRobinOffset;
  }

  const vuState = exec.vu || {};
  const iterationInScenario = finiteOrZero(vuState.iterationInScenario);
  const idInTest = Math.max(0, finiteOrZero(vuState.idInTest) - 1);
  const width = Math.max(1, finiteOrZero(roundRobinWidth), serviceCount);
  return (iterationInScenario * width) + idInTest + roundRobinOffset;
}

export function pickServiceName() {
  if (targetMode === 'fixed' && fixedServiceName) {
    return fixedServiceName;
  }

  if (targetMode === 'round-robin') {
    return serviceNameFromIndex(roundRobinIteration());
  }

  const serviceNumber = Math.floor(Math.random() * serviceCount) + 1;
  return serviceNameFromNumber(serviceNumber);
}

export function requestParams(serviceName) {
  return {
    headers: {
      Host: `${serviceName}.localhost`,
    },
    tags: {
      service: serviceName,
      target_mode: targetMode,
    },
    timeout: __ENV.E2E_REQUEST_TIMEOUT || '45s',
  };
}

export function releaseGateThresholds() {
  return {
    successRate: gateSuccessRate,
    maxFailureRate: gateMaxFailureRate,
    wakeP95Ms: gateWakeP95Ms,
    wakeP99Ms: gateWakeP99Ms,
  };
}
