import importlib.util
from pathlib import Path
import re
import unittest

spec = importlib.util.spec_from_file_location('render_k3s', Path(__file__).with_name('render_k3s.py'))
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)
IMAGE = 'registry.example/pulse-demo-target@sha256:' + 'a' * 64


class RenderK3sTests(unittest.TestCase):
    def test_demo_is_contained_and_protocol_contracts_survive(self):
        output = renderer.render('orc3-pulse-a92eda01', IMAGE)
        self.assertNotRegex(output, r'(?m)^kind: Namespace$')
        self.assertEqual(set(re.findall(r'namespace: ([a-z0-9-]+)', output)), {'orc3-pulse-a92eda01'})
        for old in ('shop', 'pulse-demo', 'pulse-system'):
            self.assertNotIn('.' + old + '.svc', output)
        self.assertNotIn('PULSE_DEMO_TARGET_IMAGE', output)
        self.assertNotIn('imagePullPolicy: Never', output)
        self.assertEqual(output.count(IMAGE), 6)
        self.assertIn('service: shop.Orders', output)
        self.assertIn('canary: orc3-pulse-a92eda01/checkout', output)
        self.assertIn('upstream: [orc3-pulse-a92eda01/catalogue]', output)
        self.assertNotIn('shop/', output)
        self.assertIn('requiredTools: ["health.check"]', output)
        self.assertIn('http://sink.orc3-pulse-a92eda01.svc:8080', output)
        self.assertIn('orders-grpc.orc3-pulse-a92eda01.svc:50051', output)
        self.assertEqual(output.count('kind: HttpCanary'), 9)
        self.assertEqual(output.count('kind: GrpcCanary'), 1)

    def test_rejects_shared_or_invalid_namespace(self):
        for namespace in ('default', 'kube-system', 'shop', 'pulse-demo', 'pulse-system', 'UPPER', 'x\ny', '-x', 'x' * 64):
            with self.subTest(namespace=namespace), self.assertRaises(ValueError):
                renderer.render(namespace, IMAGE)

    def test_rejects_mutable_or_injected_image(self):
        for image in ('target:latest', 'target@sha256:abcd', IMAGE + '\nkind: Namespace'):
            with self.subTest(image=image), self.assertRaises(ValueError):
                renderer.render('orc3-pulse-a92eda01', image)
